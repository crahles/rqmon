package main

import (
	"flag"
	"fmt"
	"sync"
	"time"

	"github.com/garyburd/redigo/redis"
)

const (
	REGULAR_CHECK_TIME      = 30 * time.Second
	TIME_BEFORE_EMAIL       = 24 * time.Hour
	TIME_BEFORE_EMAIL_AGAIN = 30 * time.Minute
	TIME_BEFORE_SMS         = 25 * time.Hour

	// Test Settings
	// REGULAR_CHECK_TIME      = 1 * time.Second
	// TIME_BEFORE_EMAIL       = 15 * time.Second
	// TIME_BEFORE_EMAIL_AGAIN = 30 * time.Second
	// TIME_BEFORE_SMS         = 1 * time.Minute
)

var (
	pool            *redis.Pool
	redisServer     = flag.String("redisServer", ":6379", "")
	redisPassword   = flag.String("redisPassword", "", "")
	resqueNamespace = flag.String("resqueNamespace", "resque:", "")
)

type Queue struct {
	Name      string
	LastEmpty time.Time
	LastEmail time.Time
	SendSMS   bool
}

type QueueMap struct {
	sync.RWMutex
	Map map[string]Queue
}

var queueMap QueueMap

func main() {
	flag.Parse()
	pool = newPool(*redisServer, *redisPassword)

	queueMap = QueueMap{
		Map: make(map[string]Queue),
	}

	for {
		RefreshQueueList()

		queueMap.RLock()
		for _, queue := range queueMap.Map {
			go CheckQueueAndAlert(queue.Name)
		}
		queueMap.RUnlock()

		time.Sleep(REGULAR_CHECK_TIME)
	}

}

func RefreshQueueList() {
	conn := pool.Get()
	defer conn.Close()
	queueMap.Lock()
	defer queueMap.Unlock()

	currentQueues := make(map[string]bool)

	// query current queues list
	queues, err := redis.Strings(conn.Do("SMEMBERS", ns("queues")))
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	// convert result slice to map for lookups
	for _, queue := range queues {
		currentQueues[queue] = true
	}

	// refresh (add new/ delete old) queues in queuesMap
	for queue, _ := range currentQueues {
		_, ok := queueMap.Map[queue]
		if ok != true {
			queueMap.Map[queue] = Queue{
				Name:      queue,
				LastEmpty: time.Now(),
				LastEmail: time.Now(),
				SendSMS:   false,
			}
		}
	}
	for _, queue := range queueMap.Map {
		_, ok := currentQueues[queue.Name]
		if ok != true {
			delete(queueMap.Map, queue.Name)
		}
	}
}

func CheckQueueAndAlert(queueName string) {
	conn := pool.Get()
	defer conn.Close()

	lenght, err := redis.Int64(conn.Do("LLEN", ns("queue:"+queueName)))
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	queueMap.Lock()
	queue, _ := queueMap.Map[queueName]

	// If queue lenght is zero everything is fine for us, just reset counters
	if lenght == 0 {
		queue.LastEmpty = time.Now()
		queue.SendSMS = false

	} else
	// queue lenght is not zero so go and check if we should alert someone
	{

		// if time since the queue was last on zero is greater than
		// TIME_BEFORE_EMAIL we send an alert via email
		// (TIME_BEFORE_EMAIL_AGAIN is our guard so we don't send on each tick)
		if time.Since(queue.LastEmpty) > TIME_BEFORE_EMAIL &&
			time.Since(queue.LastEmail) > TIME_BEFORE_EMAIL_AGAIN {
			SendAlertEmail(queueName, queue.LastEmpty)
			queue.LastEmail = time.Now()
		}

		// check if should send an one-time sms alert
		if time.Since(queue.LastEmpty) > TIME_BEFORE_EMAIL &&
			time.Since(queue.LastEmpty) > TIME_BEFORE_SMS &&
			queue.SendSMS == false {
			SendAlertSMS(queueName, queue.LastEmpty)
			queue.SendSMS = true

		}
	}

	queueMap.Map[queueName] = queue
	queueMap.Unlock()

}

func ns(key string) string {
	return *resqueNamespace + key
}

func newPool(server, password string) *redis.Pool {
	return &redis.Pool{
		MaxIdle:     3,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", server)
			if err != nil {
				return nil, err
			}
			if _, err := c.Do("AUTH", password); err != nil && password != "" {
				c.Close()
				return nil, err
			}
			return c, err
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
}
