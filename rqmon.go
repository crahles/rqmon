package main

import (
	"encoding/json"
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
	FAILURE_THRESHOLD       = 0.15

	// Test Settings
	// REGULAR_CHECK_TIME      = 1 * time.Second
	// TIME_BEFORE_EMAIL       = 15 * time.Second
	// TIME_BEFORE_EMAIL_AGAIN = 30 * time.Second
	// TIME_BEFORE_SMS         = 1 * time.Minute
	// FAILURE_THRESHOLD       = 0.15
)

var (
	pool            *redis.Pool
	redisServer     = flag.String("redisServer", ":6379", "")
	redisPassword   = flag.String("redisPassword", "", "")
	resqueNamespace = flag.String("resqueNamespace", "resque:", "")
)

type Queue struct {
	Name         string
	FailureCount int64
	LastEmpty    time.Time
	LastEmail    time.Time
	SendSMS      bool
}

type FailQueue struct {
	QueueName    string
	FailureCount int64
}

type QueueMap struct {
	sync.RWMutex
	Map map[string]Queue
}

type FailureMap struct {
	sync.RWMutex
	Map map[string]FailQueue
}

var queueMap QueueMap
var failureMap FailureMap

func main() {
	flag.Parse()
	pool = newPool(*redisServer, *redisPassword)

	queueMap = QueueMap{
		Map: make(map[string]Queue),
	}

	for {
		RefreshQueueList()
		RefreshFailedJobsList()

		queueMap.RLock()
		for _, queue := range queueMap.Map {
			go CheckQueueAndAlert(queue.Name)
			go CheckFailuresAndAlert(queue.Name)
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
		_, exists := queueMap.Map[queue]
		if exists != true {
			queueMap.Map[queue] = Queue{
				Name:         queue,
				FailureCount: 0,
				LastEmpty:    time.Now(),
				LastEmail:    time.Now(),
				SendSMS:      false,
			}
		}
	}
	for _, queue := range queueMap.Map {
		_, exists := currentQueues[queue.Name]
		if exists != true {
			delete(queueMap.Map, queue.Name)
		}
	}
}

func RefreshFailedJobsList() {
	conn := pool.Get()
	defer conn.Close()
	failureMap.Lock()
	queueMap.Lock()
	defer queueMap.Unlock()
	defer failureMap.Unlock()

	failureMap.Map = make(map[string]FailQueue)

	lenght, err := redis.Int(conn.Do("LLEN", ns("failed")))
	if err != nil {
		fmt.Println(err.Error())
		return
	}

	// iterate over all failures and count them per queueName
	for idx := 0; idx < lenght; idx++ {
		rawJson, err := redis.Bytes(conn.Do("LINDEX", ns("failed"), idx))
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		var failureEntry map[string]interface{}
		err = json.Unmarshal(rawJson, &failureEntry)
		if err != nil {
			fmt.Println(err.Error())
			return
		}

		queueName := failureEntry["queue"].(string)

		_, exists := failureMap.Map[queueName]
		if exists != true {
			failureMap.Map[queueName] = FailQueue{
				QueueName:    queueName,
				FailureCount: 1,
			}
		} else {
			failQueue := failureMap.Map[queueName]
			failQueue.FailureCount++
			failureMap.Map[queueName] = failQueue
		}
	}

	// reset fail count for non-failed queues
	for _, queue := range queueMap.Map {
		_, exists := failureMap.Map[queue.Name]
		if exists != true {
			queue.FailureCount = 0
		}
		queueMap.Map[queue.Name] = queue
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
			SendAlertEmail(queueName, queue.LastEmpty, "No Zero-Count")
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

func CheckFailuresAndAlert(queueName string) {
	failureMap.RLock()
	_, exists := failureMap.Map[queueName]
	if exists == true {
		failQueue := failureMap.Map[queueName]
		presentFailCount := failQueue.FailureCount

		queueMap.RLock()
		pastFailCount := queueMap.Map[failQueue.QueueName].FailureCount
		queueMap.RUnlock()

		var delta float64
		// fix division by zero
		if pastFailCount == 0 {
			delta = 1
		} else {
			delta = float64(presentFailCount-pastFailCount) / float64(pastFailCount)
		}

		// check if growth is larger than threshold and alert
		if delta > FAILURE_THRESHOLD {
			msg := "Failure Count Increase by %.2f%% (%d absolut)"
			msg = fmt.Sprintf(msg, delta*100, presentFailCount)
			SendAlertEmail(queueName, time.Now(), msg)
		}

		// lock queueMap for updating FailCount
		queueMap.Lock()
		queue := queueMap.Map[failQueue.QueueName]
		queue.FailureCount = presentFailCount
		queueMap.Map[failQueue.QueueName] = queue
		queueMap.Unlock()
	}
	failureMap.RUnlock()

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
