package lock

import "time"

// Options describe the options for the lock
type Options struct {
	//锁定钥匙的最长持续时间，默认值：5s
	LockTimeout time.Duration

	// 将重试获取锁的次数，默认值：0=不重试
	RetryCount int

	// RetryDelay是两次重试之间等待的时间，默认值：100ms
	RetryDelay time.Duration

	// TokenPrefix redis锁密钥的值将设置TokenPrefix+randomToken
	// 如果我们将令牌前缀设置为hostname+pid，我们就可以知道谁得到了locker
	TokenPrefix string

	Index int
}

func (o *Options) normalize() *Options {
	if o.LockTimeout < 1 {
		o.LockTimeout = 5 * time.Minute
	}
	if o.RetryCount < 0 {
		o.RetryCount = 0
	}
	if o.RetryDelay < 1 {
		o.RetryDelay = 100 * time.Millisecond
	}
	return o
}
