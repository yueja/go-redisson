package lock

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/go-redis/redis"
)

//var luaRefresh1 = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("pexpire", KEYS[1], ARGV[2]) else return 0 end`)

// var luaRelease = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`)

var luaRefresh = redis.NewScript(
	// 判断是否已经加锁
	"if (redis.call('exists', KEYS[1]) == 0) then " +
		"redis.call('hset', KEYS[1], ARGV[2], 1); " +
		"redis.call('pexpire', KEYS[1], ARGV[1]); " +
		"return 0; " +
		"end; " +

		// 锁的重入计数
		"if (redis.call('hexists', KEYS[1], ARGV[2]) == 1) then " +
		"redis.call('hincrby', KEYS[1], ARGV[2], 1); " +
		"redis.call('pexpire', KEYS[1], ARGV[1]); " +
		"return 0; " +
		"end; " +

		// 新加锁请求，互斥，返回剩余过期时间
		"return redis.call('pttl', KEYS[1]);",
)

var luaRelease = redis.NewScript(
	// 判断锁是否存在
	"if (redis.call('hexists', KEYS[1], ARGV[2]) == 0) then return 0;end;" +
		// 锁存在进行计数-1
		"local counter = redis.call('hincrby', KEYS[1], ARGV[2], -1); " +
		// 如果剩余计数>0，则重置过期时间
		"if (counter > 0) then redis.call('pexpire', KEYS[1], ARGV[1]); return 0; " +
		// 否则，删除key
		"else redis.call('del', KEYS[1]); return 0; end; ",
)

var luaDog = redis.NewScript(
	"if (redis.call('exists', KEYS[1]) == 1) then " +
		"redis.call('pexpire', KEYS[1], ARGV[1]); " +
		"return 1; " +
		"else return 0; " +
		"end; ",
)

var emptyCtx = context.Background()

// ErrLockNotObtained may be returned by Obtain() and Run()
// if a lock could not be obtained.
var (
	ErrLockUnlockFailed     = errors.New("lock unlock failed")
	ErrLockNotObtained      = errors.New("lock not obtained")
	ErrLockDurationExceeded = errors.New("lock duration exceeded")
)

// RedisClient is a minimal client interface.
type RedisClient interface {
	SetNX(key string, value interface{}, expiration time.Duration) *redis.BoolCmd
	Eval(script string, keys []string, args ...interface{}) *redis.Cmd
	EvalSha(sha1 string, keys []string, args ...interface{}) *redis.Cmd
	ScriptExists(scripts ...string) *redis.BoolSliceCmd
	ScriptLoad(script string) *redis.StringCmd
}

// Locker allows (repeated) distributed locking.
type Locker struct {
	client RedisClient
	key    string
	opts   Options

	token    string
	dogTimer *time.Timer
	mutex    sync.Mutex
}

// Run runs a callback handler with a Redis lock. It may return ErrLockNotObtained
// if a lock was not successfully acquired.
func Run(client RedisClient, key string, opts *Options, handler func()) error {
	locker, err := Obtain(client, key, opts)
	if err != nil {
		return err
	}

	sem := make(chan struct{})
	go func() {
		handler()
		close(sem)
	}()

	select {
	case <-sem:
		return locker.Unlock()
	case <-time.After(locker.opts.LockTimeout):
		return ErrLockDurationExceeded
	}
}

// Obtain 是New().Lock()的快捷方式。如果未成功获取锁，它可能返回ErrLockNotObserved
func Obtain(client RedisClient, key string, opts *Options) (*Locker, error) {
	locker := New(client, key, opts)
	if ok, err := locker.Lock(); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrLockNotObtained
	}
	return locker, nil
}

// New 在给定的key上创建一个新的分布式储物柜。
func New(client RedisClient, key string, opts *Options) *Locker {
	var o Options
	if opts != nil {
		o = *opts
	}
	o.normalize()
	return &Locker{client: client, key: key, opts: o}
}

// IsLocked 如果锁仍被持有，则IsLocked返回true。
func (l *Locker) IsLocked() bool {
	l.mutex.Lock()
	locked := l.token != ""
	l.mutex.Unlock()

	return locked
}

// Lock 锁，不要忘记在使用后推迟Unlock（）函数来释放锁。
func (l *Locker) Lock() (bool, error) {
	return l.LockWithContext(emptyCtx)
}

// LockWithContext 类似于Lock，允许传递一个允许取消的附加上下文
func (l *Locker) LockWithContext(ctx context.Context) (bool, error) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.token != "" {
		// 锁的重入
		return l.obtain(ctx, l.token)
	}
	return l.create(ctx)
}

// 看门狗，锁续命
func (l *Locker) dog(ctx context.Context) {
	if l.dogTimer != nil {
		l.dogTimer.Stop()
	}
	l.dogTimer = time.NewTimer((l.opts.LockTimeout / 3) * 2) // 过期时间还剩余1/3时候开始启动看门狗锁续命流程
	// 利用count避免解锁失败，导致无限锁续命
	count := 0
	for {
		select {
		case <-ctx.Done():
			l.dogTimer.Stop()
			return
		case <-l.dogTimer.C:
			//fmt.Println("打印看门狗触发时间：", time.Now().Unix())
			ttl := strconv.FormatInt(int64(l.opts.LockTimeout/time.Millisecond), 10)
			data, err := luaDog.Run(l.client, []string{l.key}, ttl).Result()
			if err != nil {
				return
			}
			log.Printf("打印看门狗触发时间%d：%d,count:%d,data:%d，token:%s", l.opts.Index, time.Now().Unix(), count, data, l.token)
			if data == int64(1) && count < 10 {
				//	锁续命重置了,重新启动新的定时任务
				count++
				l.dogTimer = time.NewTimer((l.opts.LockTimeout / 3) * 2)
			} else {
				l.dogTimer.Stop()
				break
			}
		}
	}
}

// Unlock 释放锁
func (l *Locker) Unlock() error {
	l.mutex.Lock()
	err := l.release()
	l.mutex.Unlock()

	return err
}

func (l *Locker) create(ctx context.Context) (bool, error) {
	l.reset()

	// Create a random token
	token, err := randomToken()
	if err != nil {
		return false, err
	}
	token = l.opts.TokenPrefix + token

	// Calculate the timestamp we are willing to wait for
	attempts := l.opts.RetryCount + 1

	// 定时任务执行重试
	var retryDelay *time.Timer
	defer func() {
		if retryDelay != nil {
			retryDelay.Stop()
		}
	}()

	for {
		// 重试机制
		ok, err := l.obtain(ctx, token)
		if err != nil {
			return false, err
		} else if ok {
			l.token = token
			return true, nil
		}

		if attempts--; attempts <= 0 {
			return false, nil
		}

		if retryDelay == nil {
			retryDelay = time.NewTimer(l.opts.RetryDelay)
		} else {
			retryDelay.Reset(l.opts.RetryDelay)
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-retryDelay.C:
			log.Printf("开始重试加锁%d：%d", l.opts.Index, time.Now().UnixMilli())
		}
	}
}

func (l *Locker) obtain(ctx context.Context, token string) (bool, error) {
	ttl := strconv.FormatInt(int64(l.opts.LockTimeout/time.Millisecond), 10)
	result, err := luaRefresh.Run(l.client, []string{l.key}, ttl, token).Result()
	if err != nil {
		return false, err
	}
	if result != int64(0) {
		// 锁已经被占用
		return false, nil
	}
	go l.dog(ctx)
	log.Printf("锁获取成功%d", l.opts.Index)
	return true, err
}

func (l *Locker) release() error {
	ttl := strconv.FormatInt(int64(l.opts.LockTimeout/time.Millisecond), 10)
	err := luaRelease.Run(l.client, []string{l.key}, ttl, l.token).Err()
	if err != nil {
		return err
	}
	return err
}

func (l *Locker) reset() {
	l.token = ""
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}
