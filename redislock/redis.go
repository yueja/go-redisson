package redislock

import (
	"errors"
	"github.com/go-redis/redis"
)

var (
	// 初始化的redis的db
	_redisDb = []int{0, 2, 6, 10}
	// 默认的redis连接
	_defaultRedis *redis.Client
	// redis map
	_redisMap = make(map[int]*redis.Client)
)

const (
	// 默认使用的redis连接的db
	_defaultRedisDB = 0
	// RedisNil redis nil error
	RedisNil = redis.Nil
)

func InitRDB() error {
	for _, v := range _redisDb {
		redisOptions := &redis.Options{
			ReadTimeout: -1,
		}
		redisOptions.Addr = "localhost:6379"
		redisOptions.DB = v
		redisOptions.PoolSize = 10
		client := redis.NewClient(redisOptions)
		_, err := client.Ping().Result()
		if err != nil {
			return err
		}
		_redisMap[v] = client
		if v == _defaultRedisDB {
			_defaultRedis = client
		}
	}
	if _defaultRedis == nil {
		return errors.New("default redis is nil")
	}
	return nil
}

func init() {
	InitRDB()
}

// Lock
/**
 * redis 分布式锁
 * @param  key  {string} 			 初始化锁的互斥标示
 * @param  opts {*redislock.Options} 初始化锁的配置信息（详见该机构提的配置注释）
 * @return *    {*redislock.Options} 锁的实例（业务逻辑完成之后需要解锁）
 * @return *    {error} 			 异常
 */
func Lock(key string, opts *Options, dbIndex ...int) (*Locker, error) {
	locker, err := Obtain(getRedisByIndexDB(dbIndex...), key, opts)
	if err != nil {
		return locker, err
	}
	if locker == nil {
		return locker, errors.New("could not obtain lock")
	}
	return locker, nil
}

// UnLock
/**
 * redis 分布式锁 -（解锁）
 * @param  locker {*redislock.Locker} 锁的实例（初始化的时候得到）
 */
func UnLock(locker *Locker, dbIndex ...int) {
	if locker == nil {
		return
	}
	err := locker.Unlock()
	if err != nil {
		return
	}
}

func getRedisByIndexDB(dbIndex ...int) *redis.Client {
	if len(dbIndex) == 0 || dbIndex[0] == _defaultRedisDB {
		return _defaultRedis
	}
	if r, ok := _redisMap[dbIndex[0]]; ok {
		return r
	} else {
		panic("redis client not init")
	}
}
