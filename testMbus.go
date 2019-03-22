package mbus

// simple test of the mbus package

import (
	"math/rand"
	"fmt"
	"strconv"
	"sync"
	"time"
)

var wg sync.WaitGroup
// consumer id receives messages whose topic (as an integer) is divisible by id
func consumer (id int, c <-chan PMsg) {
	for {
		x, ok := <- c
		if !ok {
			break
		}
		fmt.Printf("%03d got %s = %s\n", id, x.Topic, x.Msg)
		wg.Done()
		time.Sleep(time.Duration(rand.Intn(500)) * time.Microsecond)
	}
}

// publisher id generates n messages with random topics from 1 to 100
func producer (id int, n int, mb *Mbus) {
	// publish n messages on random topics between 1 and 100
	for i:= 1; i <= n; i++ {
		N := rand.Intn(100) + 1
		// count the number of consumers who should receive the message
		nm := 0
		for j := 1; j <= 30; j++ {
			if N % j == 0 {
				nm++
			}
		}
		m := Msg{Topic(strconv.Itoa(N)), fmt.Sprintf("#%d from producer %d", i, id)}
		fmt.Printf("published %s = %s\n", m.Topic, m.Msg)
		wg.Add(nm)
		mb.Pub(m)
		time.Sleep(time.Duration(rand.Intn(500)) * time.Microsecond)
	}
}

func Test() {
	rand.Seed(time.Now().UnixNano())
	mb := NewMbus()
	// generate 30 random consumers; consumer i subscribes to
	// messages whose topic is an integer multiple of i <= 100
	for i:= 1; i <= 30; i++ {
		c := mb.NewSubr(Topic(strconv.Itoa(i)), Topic(strconv.Itoa(2*i)))
		for j:= i*3; j <= 100; j += i {
			c.Sub(Topic(strconv.Itoa(j)))
		}
		if i == 12 {
			fmt.Printf("consumer %d wants these %d topics:\n", i, c.NumTopics())
			for _,t := range c.Topics() {
				fmt.Printf("   %s\n", t)
			}
		}
		go consumer(i, c.Msgs())
	}
	for i:= 1; i <= 30; i++ {
		go producer(i, i, &mb)
	}
	// can do only one of the two following
	if rand.Intn(2) == 1 {
		// sleep for one second so producers can add to wait group
		time.Sleep(time.Second)
		fmt.Println("Slept for 1s; now waiting for all messages to be received")
		wg.Wait()
	} else {
		// sleep a shorter time, then close
		time.Sleep(time.Millisecond * 10)
		fmt.Println("Slept for 10 ms; now closing mbus")
		mb.Close()
	}
}
