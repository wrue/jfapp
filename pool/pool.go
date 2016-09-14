package pool

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

type (
	// 资源池（应设置最大容量）
	Pool interface {
		//资源销毁
		Close()
		//返回资源池大小
		Len() int

		// 调用资源池中的资源
		Call(func(Src) error) error
	}

	classic struct {
		srcs     chan Src      // 资源channel列表(Src须为指针类型)
		capacity int           // 资源池容量
		maxIdle  int           // 资源最大空闲数
		len      int           // 当前资源数
		factory  Factory       // 创建资源的方法
		gctime   time.Duration // 空闲资源回收时间
		closed   bool          // 标记是否已关闭资源池
		sync.RWMutex
	}

	Src interface {
		// 判断资源是否可用
		Usable() bool

		// 使用后的重置方法
		Reset()

		// 被资源池删除前的自毁方法
		Close()
	}

	// 创建资源的方法
	Factory func() (Src, error)
)

const (
	GC_TIME = 60e9
)

var (
	closedError  = errors.New("资源池已关闭")
	timeOutError = errors.New("获取资源超时")
)

func ClassicPool(capacity, maxIdle int, factory Factory, gctime ...time.Duration) Pool {
	if len(gctime) == 0 {
		gctime = append(gctime, GC_TIME)
	}
	pool := &classic{
		srcs:     make(chan Src, capacity),
		capacity: capacity,
		maxIdle:  maxIdle,
		factory:  factory,
		gctime:   gctime[0],
		closed:   false,
	}
	go pool.gc()
	return pool
}

func (self *classic) Call(callBack func(Src) error) (err error) {
	var src Src

	timeout := make(chan bool, 1)
	go func() {
		time.Sleep(10e9) // 等待1秒钟
		timeout <- true
	}()

wait:
	self.RLock()
	if self.closed {
		self.RUnlock()
		return closedError
	}
	select {
	case src = <-self.srcs:
		self.RUnlock()
		if !src.Usable() {
			self.del(src)
			goto wait
		}
	case <-timeout:
		self.RUnlock()
		return timeOutError
	default:
		self.RUnlock()
		err = self.incAuto()
		if err != nil {
			return err
		}
		runtime.Gosched()
		goto wait
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
		self.recover(src)
	}()
	err = callBack(src)
	return err
}

// 销毁资源池
func (self *classic) Close() {
	self.Lock()
	defer self.Unlock()
	if self.closed {
		return
	}
	self.closed = true
	for i := len(self.srcs); i >= 0; i-- {
		(<-self.srcs).Close() //关闭资源
	}
	close(self.srcs) //关闭channel
	self.len = 0
}

// 空闲资源回收协程
func (self *classic) gc() {
	for !self.isClosed() {
		self.Lock()
		extra := len(self.srcs) - self.maxIdle
		if extra > 0 {
			self.len -= extra
			for ; extra > 0; extra-- {
				(<-self.srcs).Close()
			}
		}
		self.Unlock()
		time.Sleep(self.gctime)
	}
}

// 返回当前资源数量
func (self *classic) Len() int {
	self.RLock()
	defer self.RUnlock()
	return self.len
}

//资源不够时，自动创建新资源并添加到池中
func (self *classic) incAuto() error {
	self.Lock()
	defer self.Unlock()
	if self.len >= self.capacity {
		return nil
	}
	src, err := self.factory()
	if err != nil {
		return err
	}
	self.srcs <- src
	self.len++
	return nil
}

func (self *classic) isClosed() bool {
	self.RLock()
	defer self.RUnlock()
	return self.closed
}

func (self *classic) del(src Src) {
	src.Close()
	self.Lock()
	self.len--
	self.Unlock()
}

func (self *classic) recover(src Src) {
	self.RLock()
	defer self.RUnlock()
	if self.closed {
		return
	}
	src.Reset()
	self.srcs <- src
}
