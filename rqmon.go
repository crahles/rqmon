package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/garyburd/redigo/redis"
)

const (
	REGULAR_CHECK_TIME      = 15 * time.Second
	TIME_BEFORE_ALERT       = 6 * time.Hour
	TIME_BEFORE_ALERT_AGAIN = 30 * time.Minute
	FAILURE_THRESHOLD       = 0.10

	// Test Settings
	// REGULAR_CHECK_TIME      = 2 * time.Second
	// TIME_BEFORE_ALERT       = 10 * time.Second
	// TIME_BEFORE_ALERT_AGAIN = 20 * time.Second
	// FAILURE_THRESHOLD       = 0.15
)

var (
	pool *redis.Pool

	logFile = flag.String("logFile", "logpath/file.log", "")

	redisServer   = flag.String("redisServer", ":6379", "")
	redisPassword = flag.String("redisPassword", "", "")

	resqueNamespace = flag.String("resqueNamespace", "resque:", "")
	resqueWebUrl    = flag.String("resqueWebUrl", "http://resque.example.com", "")

	smtpServer   = flag.String("smtpServer", "smtp.gmail.com", "")
	smtpPort     = flag.String("smtpPort", "587", "")
	smtpUsername = flag.String("smtpUsername", "me@example.com", "")
	smtpPassword = flag.String("smtpPassword", "Passw0rd", "")

	alertFrom      = flag.String("alertFrom", "me@example.com", "")
	alertRecipient = flag.String("alertRecipient", "me@example.com", "")

	showVersion = flag.Bool("version", false, "Show version and exit")
	Commit      = "dirty"
	Version     = "DEV"
)

type metric struct {
	O string
	C int64
}

type failedJob struct {
	FailedAt string `json:"failed_at"`
	Payload  failedJobPayload
	Queue    string
	Worker   string
}

type failedJobPayload struct {
	Class string
}

type alert struct {
	Message string
	WebLink string
	Metric  metric
}

func versionAndExit() {
	fmt.Printf("v%s-%s\n", Version, Commit)
	os.Exit(0)
}

func main() {
	flag.Parse()

	if *showVersion {
		versionAndExit()
	}

	SetupLogger()
	pool = newPool(*redisServer, *redisPassword)

	log.Printf("\nRQMon starting...\n\n")

	alertHandler := make(chan alert)
	go handleAlerts(alertHandler)

	a := alertFailureTrend()
	b := alertNotEmpty()
	for {
		select {
		case m := <-a:
			msg := fmt.Sprintf("%s's failure trend is rising", m.O)
			link := fmt.Sprintf("%s/cleaner_list?c=%s", *resqueWebUrl, m.O)
			alertHandler <- alert{msg, link, m}
		case m := <-b:
			msg := fmt.Sprintf("%s's queue length isn't shrinking", m.O)
			link := fmt.Sprintf("%s/overview", *resqueWebUrl)
			alertHandler <- alert{msg, link, m}
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
				fmt.Sprintf("%s (Count: %d).", a.Message, a.Metric.C),
				a.WebLink,
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

		// special cleanup message
		c <- metric{"cleanup", -1}

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
			if l.O == "cleanup" && l.C == -1 {
				removeOrphanQueues(&queues)
				continue
			}

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

		// special cleanup message
		c <- metric{"cleanup", -1}

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
			if f.O == "cleanup" && f.C == -1 {
				removeOrphanJobs(&lastCounts)
				continue
			}

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

func removeOrphanQueues(queues *map[string]time.Time) {
	conn := pool.Get()
	defer conn.Close()

	slice, err := redis.Strings(conn.Do("SMEMBERS", ns("queues")))
	if !ok(err) {
		return
	}

	list := make(map[string]struct{})
	for _, v := range slice {
		list[v] = struct{}{}
	}

	for key, _ := range *queues {
		if _, exists := list[key]; !exists {
			delete(*queues, key)
		}
	}
}

func removeOrphanJobs(lastCounts *map[string]int64) {
	conn := pool.Get()
	defer conn.Close()

	list := make(map[string]struct{})
	objs, err := redis.Strings(conn.Do("LRANGE", ns("failed"), "0", "-1"))
	if !ok(err) {
		return
	}

	for _, o := range objs {
		list[jobName(o)] = struct{}{}
	}

	for key, _ := range *lastCounts {
		if _, exists := list[key]; !exists {
			delete(*lastCounts, key)
		}
	}
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

	if *logFile != "" {
		file, err := os.OpenFile(
			*logFile,
			os.O_APPEND|os.O_WRONLY|os.O_CREATE,
			0755,
		)
		if err != nil {
			log.Println("Couldn't open log file, falling back to STDOUT!")
		} else {
			log.SetOutput(file)
		}
	} else {
		log.SetOutput(ioutil.Discard)
	}
}
