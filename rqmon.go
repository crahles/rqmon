package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/garyburd/redigo/redis"
)

const (
	REGULAR_CHECK_TIME      = 30 * time.Second
	TIME_BEFORE_ALERT       = 24 * time.Hour
	TIME_BEFORE_ALERT_AGAIN = 30 * time.Minute
	FAILURE_THRESHOLD       = 0.15

	// Test Settings
	// REGULAR_CHECK_TIME      = 2 * time.Second
	// TIME_BEFORE_ALERT       = 10 * time.Second
	// TIME_BEFORE_ALERT_AGAIN = 20 * time.Second
	// FAILURE_THRESHOLD       = 0.15
)

var (
	pool          *redis.Pool
	redisServer   = flag.String("redisServer", ":6379", "")
	redisPassword = flag.String("redisPassword", "", "")

	resqueNamespace = flag.String("resqueNamespace", "resque:", "")

	smtpServer   = flag.String("smtpServer", "smtp.gmail.com", "")
	smtpPort     = flag.String("smtpPort", "587", "")
	smtpUsername = flag.String("smtpUsername", "me@example.com", "")
	smtpPassword = flag.String("smtpPassword", "Passw0rd", "")

	alertFrom      = flag.String("alertFrom", "me@example.com", "")
	alertRecipient = flag.String("alertRecipient", "me@example.com", "")
)

type metric struct {
	O string
	C int64
}

type failedJob struct {
	FailedAt  string `json:"failed_at"`
	Payload   failedJobPayload
	Exception string
	Queue     string
	Worker    string
}

type failedJobPayload struct {
	Class string
	Args  string
}

type alert struct {
	Message string
	Metric  metric
}

func main() {
	flag.Parse()
	SetupLogger()
	pool = newPool(*redisServer, *redisPassword)

	log.Println("RQMon started...")

	alertHandler := make(chan alert)
	go handleAlerts(alertHandler)

	a := alertFailureTrend()
	b := alertNotEmpty()
	for {
		select {
		case m := <-a:
			alertHandler <- alert{"A job's failure trend is rising", m}
		case m := <-b:
			alertHandler <- alert{"A queue's length isn't shrinking", m}
		}
	}
}

func handleAlerts(c <-chan alert) {
	alertMap := make(map[string]time.Time)

	for {
		a := <-c
		_, exists := alertMap[a.Metric.O]
		if !exists || time.Since(alertMap[a.Metric.O]) > TIME_BEFORE_ALERT_AGAIN {
			alertMap[a.Metric.O] = time.Now()
			SendAlertByEmail(
				a.Message,
				fmt.Sprintf(
					"%s (Object: %s, Count: %d)\n",
					a.Message, a.Metric.O, a.Metric.C,
				),
			)

		}
	}
}

func pollLengths(c chan<- metric) {
	ticker := time.NewTicker(REGULAR_CHECK_TIME)
	conn := pool.Get()
	defer conn.Close()

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

func alertNotEmpty() chan metric {
	lens := make(chan metric)
	go pollLengths(lens)

	c := make(chan metric)

	go func() {
		queues := make(map[string]time.Time)

		for {
			l := <-lens
			_, exists := queues[l.O]
			if !exists || l.C == 0 {
				queues[l.O] = time.Now()
				continue
			}
			if time.Since(queues[l.O]) > TIME_BEFORE_ALERT {
				c <- l
			}
		}

	}()

	return c
}

func pollFailed(c chan<- metric) {
	ticker := time.NewTicker(REGULAR_CHECK_TIME)
	conn := pool.Get()
	defer conn.Close()

	for {
		counts := make(map[string]int64)

		objs, err := redis.Strings(conn.Do("LRANGE", ns("failed"), "0", "-1"))
		if !ok(err) {
			<-ticker.C
			continue
		}

		for _, o := range objs {
			if _, exists := counts[jobName(o)]; !exists {
				counts[jobName(o)] = 1
				continue
			}
			counts[jobName(o)] += 1
		}

		for job, count := range counts {
			c <- metric{job, count}
		}

		<-ticker.C
	}
}

func alertFailureTrend() chan metric {

	fails := make(chan metric)
	go pollFailed(fails)

	c := make(chan metric)

	go func() {
		lastCounts := make(map[string]int64)
		for {
			f := <-fails
			if _, exists := lastCounts[f.O]; !exists {
				lastCounts[f.O] = f.C
				continue
			}

			if deltaGtFailureThreshold(f.C, lastCounts[f.O]) {
				c <- f
			}
			lastCounts[f.O] = f.C
		}
	}()

	return c
}

func jobName(object string) string {
	var failedJob failedJob
	err := json.Unmarshal([]byte(object), &failedJob)
	if !ok(err) {
		return "unknown"
	}
	if failedJob.Payload.Class != "" {
		return failedJob.Payload.Class
	}
	return "unknown"
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

func SetupLogger() {
	log.SetFlags(log.Lmicroseconds | log.Lshortfile)
	hostname, _ := os.Hostname()
	prefix := "[" + hostname + "] "
	log.SetPrefix(fmt.Sprintf("%spid:%d ", prefix, syscall.Getpid()))
}
