### mbus ###
simple message bus for Go

A message bus on the publish/subscribe model, using dynamic queues
from [github.com/foize/go.fifo](https://github.com/foize/go.fifo)

**Features**:

- messages are received on a `chan`, so subscribers don't need to poll
- the subscriber's `chan` is closed when the message bus is closed
- publishers never block waiting for subscribers to handle messages
- no arbitrary limits on numbers of subscribers, topics, subscriptions, or messages
- for any subscriber, messages are received in the order they were published
  (more correctly, in the order they arrived at the publish queue locking step).
  We don't use a chan for publishing messages because there's no language
  guarantee about the order in which blocked writers would be unblocked.
- no dropped messages (unless user closes message bus before all published
  messages are received)
- queues hold only pointers to messages, so duplication among queues is lightweight

**Resource use**:
- per messagebus: one goroutine, one fifo
- per subscriber: one goroutine, one fifo
- per subscribed topic: one map from subscriber pointers to bool
- per *subscription* (i.e. `(subscriber, topic)`): one map entry (subscriber pointer)
- per published but unread message: one fifo node containing a pointer to a message

**Message Flow**:
```
 publisher ----+                      +-------> subscriber queue (1) --[viewer]--> chan for subscriber 1
               |                      |
               v                      |
 publisher -> publish queue -----[bcaster]----> subscriber queue (2) --[viewer]--> chan for subscriber 2
               ^                      |
               |                      |
 publisher ----+                      +-------> subscriber queue (3) --[viewer]--> chan for subscriber 3
```
**`[bcaster]`** is a goroutine to move/copy each message from the publish queue to the queues of all interested subscribers
**`[viewer]`** is a goroutine to move messages from a subscriber's queue to its channel

**Example**:
```go
package main
import (
	"github.com/jbrzusto/mbus"
	"fmt"
	"time"
)

func main() {
	mb := mbus.NewMbus()
	optimist := mb.Sub("good news")
	pessimist := mb.Sub("bad news")
	realist := mb.Sub("*")
	opponent := mb.Sub("bad news")
	// come to think of it:
	opponent.Sub("fake news")
	go consume("I", realist)
	go consume("you", pessimist)
	go consume("sonny", optimist)
	go consume("they", opponent)

	mb.Pub(mbus.Msg{"bad news", "I fell out of a plane"})
	mb.Pub(mbus.Msg{"good news", "I was wearing a parachute"})
	mb.Pub(mbus.Msg{"fake news", "The plane is not actually in the air"})

	time.Sleep(time.Millisecond * 50)

	// I need a break from this:
	realist.Unsub("*")
	mb.Pub(mbus.Msg{"bad news", "The parachute did not deploy"})
	mb.Pub(mbus.Msg{"good news", "I landed on a trampoline"})
	mb.Pub(mbus.Msg{"fake news", "Humans can fly"})

	time.Sleep(time.Millisecond * 50)
	mb.Close()
}

func consume(who string, s *mbus.Subr) {
	for {
		msg, ok := <- s.Msgs()
		if ! ok {
			break
		}
		fmt.Printf("%s got %s: %s\n", who, msg.Topic, msg.Msg)
	}
}
```
