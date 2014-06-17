package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/garyburd/redigo/redis"
)

type metric struct {
	Q string
	N int64
}

func allTheThings() {
	a := alertFailureTrend()
	b := alertNotEmpty()
	for {
		select {
		case <-a:
			fmt.Println("A queue's failure trend is rising")
		case <-b:
			fmt.Println("A queue's length isn't shrinking")
		}
	}
}

// alertFailureTrend sends a struct{} on the returned channel
// when it passes a thershhold
func alertFailureTrend() chan struct{} {

	fails := make(chan metric)
	go pollFailed(fails)

	c := make(chan struct{})

	go func() {
		var lastCounts map[string]int64
		for {
			f := <-fails
			if deltaGtFailureThreshold(f.N, lastCounts[f.Q]) {
				c <- struct{}{}
			}
			lastCounts[f.Q] = f.N
		}
	}()

	return c
}

func alertNotEmpty() chan struct{} {
	lens := make(chan metric)
	go pollLengths(lens)

	c := make(chan struct{})

	go func() {
		var lastEmpty map[string]time.Time

		for {
			l := <-lens
			if l.N == 0 {
				lastEmpty[l.Q] = time.Now()
				continue
			}
			if time.Since(lastEmpty[l.Q]) > TIME_BEFORE_EMAIL {
				c <- struct{}{}
			}
		}

	}()

	return c
}

func pollLengths(c chan<- metric) {
	ticker := time.NewTicker(REGULAR_CHECK_TIME)
	conn := pool.Get()

	for {
		queues, err := redis.Strings(conn.Do("SMEMBERS", ns("queues")))
		if !ok(err) {
			<-ticker.C
			continue
		}

		for _, q := range queues {
			n, err := redis.Int64(conn.Do("LLEN", ns("queue:"+q)))
			if ok(err) {
				c <- metric{q, n}
			}
		}

		<-ticker.C
	}
}

func pollFailed(c chan<- metric) {
	ticker := time.NewTicker(REGULAR_CHECK_TIME)
	conn := pool.Get()

	for {
		var counts map[string]int64
		objs, err := redis.Strings(conn.Do("LRANGE", ns("failed")))
		if !ok(err) {
			<-ticker.C
			continue
		}

		for _, o := range objs {
			counts[queueName(o)] += 1
		}

		for q, n := range counts {
			c <- metric{q, n}
		}

		<-ticker.C
	}
}

func queueName(object string) string {
	var failureEntry map[string]interface{}
	err := json.Unmarshal([]byte(object), &failureEntry)
	if !ok(err) {
		return ""
	}

	return failureEntry["queue"].(string)
}

func ok(err error) bool {
	if err != nil {
		log.Print(err)
		return false
	}
	return true
}

func deltaGtFailureThreshold(a, b int64) bool {
	return float64(a) > float64(b)*(1+FAILURE_THRESHOLD)
}
