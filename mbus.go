// simple message bus on pub/sub model, with these features:
// - publish never blocks waiting for subscribers
// - messages are received from a channel, which gets closed when the messagebus
//   is destroyed
// - for any subscriber, messages are received in the order they were published
// - delivery (to a subscriber's queue) is guaranteed;
// - uses one goroutine per subscriber plus one goroutine for the message bus
// - messages are stored by pointer, so queues are lightweight
//
// Message Flow:
//
// publisher ----+                      +-------> subscriber queue (1) --[viewer]--> chan for subscriber 1
//               |                      |
//               v                      |
// publisher -> publish queue -----[bcaster]----> subscriber queue (2) --[viewer]--> chan for subscriber 2
//               ^                      |
//               |                      |
// publisher ----+                      +-------> subscriber queue (3) --[viewer]--> chan for subscriber 3
//
// [bcaster] is a goroutine to move each message from the publish queue to the queues of all interested subscribers
// [viewer] is a goroutine to move messages from a subscriber's queue to its channel
//

package mbus

import (
	"github.com/foize/go.fifo"
	"sync"
)

// The type used for Message topics.
type Topic string

// Messages have a topic and an arbitrary object.
type Msg struct {
	Topic Topic
	Msg   interface{}
}

// Queues hold pointers to messages
type PMsg *Msg

// The mostly opaque type representing a subscriber.  Clients get to see
// a read-only version of the message channel, via Msgs()
type Subr struct {
	msgs *fifo.Queue // queue of messages waiting to be read by client
	mchn chan PMsg   // subscriber receives messages on here
	wake chan bool   // viewer goroutine reads from this to learn of new messages
	mbus *Mbus       // message bus this subscriber is attached to
}

// Get a subscriber's read-only message channel
func (sub *Subr) Msgs() <-chan PMsg {
	return sub.mchn
}

// it would be nice to define `type MsgChan chan Msg` and then
// create a read-only variant, but go doesn't currently allow this;
// see: https://github.com/golang/go/issues/21953

// a type representing the message bus
type Mbus struct {
	msgs     *fifo.Queue              // queue of messages published but not yet distributed
	subs     map[*Subr]bool           // set of pointers to subscribers
	wants    map[Topic]map[*Subr]bool // set of pointers to subscribers for each Topic
	subsLock sync.Mutex               // lock for subscriber modifications
	wakeLock sync.Mutex               // lock for bcaster channel
	wake     chan bool                // bcaster goroutine reads from this to learn of new messages
	closed   bool                     // false until Close() is called.
}

// constructor for a message bus
func NewMbus() (mb Mbus) {
	mb = Mbus{msgs: fifo.NewQueue(),
		subs:   make(map[*Subr]bool, 10),
		wants:  make(map[Topic]map[*Subr]bool),
		wake:   make(chan bool, 1),
		closed: false}
	// start the goroutine to distribute messages from the publish queue to the
	// subscriber queues
	go mb.bcaster()
	return
}

// tell message bus it is no longer needed
//
// This will end the bcaster and all viewer goroutines.
// All subscribers will have their message channels closed.
// After calling this method, calls to NewSubr, Sub, Unsub, Pub, and
// Close will do nothing.
func (mb *Mbus) Close() {
	mb.wakeLock.Lock()
	defer mb.wakeLock.Unlock()
	if mb.closed {
		return
	}
	close(mb.wake)
	mb.subsLock.Lock()
	defer mb.subsLock.Unlock()
	for sub, _ := range mb.subs {
		close(sub.wake)
	}
	mb.closed = true
}

// create a subscriber to one or more topics of interest
//
// As a special case, the Topic "*" is interpreted to mean
// that *all* messages should be received.  If a client
// has subscribed to "*", then all messages are received
// until the client unsubscribes from "*"; i.e. individual
// Sub / Unsub calls have no effect.
func (mb *Mbus) NewSubr(topic ...Topic) (sub *Subr) {
	if mb.closed {
		return nil
	}
	mb.subsLock.Lock()
	defer mb.subsLock.Unlock()
	sub = &Subr{msgs: fifo.NewQueue(), mchn: make(chan PMsg), wake: make(chan bool, 1), mbus: mb}
	for _, t := range topic {
		if t == "*" {
			mb.suball(sub)
			break
		}
		mb.sub(t, sub)
	}
	mb.subs[sub] = true
	// start the goroutine that moves messages from the queue (where the bcaster puts them)
	// to the subscriber's message channel
	go sub.viewer()
	return
}

// subscribe to additional topics
//
// Returns true on success, false otherwise.
//
// As a special case, the Topic "*" is interpreted to mean
// that *all* topics should be subscribed to; i.e. all messages
// should be received, even if they are for topics not yet known.
func (sub *Subr) Sub(topic ...Topic) bool {
	mb := sub.mbus
	if mb.closed {
		return false
	}
	mb.subsLock.Lock()
	defer mb.subsLock.Unlock()
	for _, t := range topic {
		if t == "*" {
			mb.suball(sub)
			break
		}
		mb.sub(t, sub)
	}
	return true
}

// unsubscribe from one or more topics
//
// Returns true on sucess (i.e. the Subscriber's list of topics no
// longer includes those specified), false otherwise.  It is not an
// error to unsubscribe to a topic which was not already subscribed
// to.  Note that a message channel can exist without any
// subscriptions.  Reads from it will then always block.
//
// As a special case, the Topic "*" is interpreted to mean
// that *all* topics should be unsubscribed from.

func (sub *Subr) Unsub(topic ...Topic) bool {
	mb := sub.mbus
	if mb.closed {
		return false
	}
	mb.subsLock.Lock()
	defer mb.subsLock.Unlock()
	for _, t := range topic {
		if t == "*" {
			mb.unsuball(sub)
			break
		}
		mb.unsub(t, sub)
	}
	return true
}

// add a subscriber to a topic
//
// Only used by internal callers who have
// locked mb.subsLock
func (mb *Mbus) sub(topic Topic, sub *Subr) {
	_, ok := mb.wants[topic]
	if !ok {
		mb.wants[topic] = make(map[*Subr]bool)
	}
	mb.wants[topic][sub] = true
}

// add a subscriber to the "*" topic so it will receive all messages.
//
// This also removes any non-* subscriptions for sub, to prevent
// it from receiving duplicate messages.
//
// Only used by internal callers who have
// locked mb.subsLock
func (mb *Mbus) suball(sub *Subr) {
	for t, _ := range mb.wants {
		mb.unsub(t, sub)
	}
	mb.sub("*", sub)
}

// remove a subscriber from a topic, which can be "*"
//
// Only used by internal callers who have
// locked mb.subsLock
func (mb *Mbus) unsub(topic Topic, sub *Subr) {
	_, ok := mb.wants[topic]
	if !ok {
		return
	}
	delete(mb.wants[topic], sub)
	if len(mb.wants[topic]) == 0 {
		delete(mb.wants, topic)
	}
}

// remove a subscriber from all topics, including "*"
//
// only used by internal callers who have
// locked mb.subsLock
func (mb *Mbus) unsuball(sub *Subr) {
	for t, x := range mb.wants {
		delete(x, sub)
		if len(x) == 0 {
			delete(mb.wants, t)
		}
	}
}

// get number of topics subscribed to
//
func (sub *Subr) NumTopics() int {
	mb := sub.mbus
	if mb.closed {
		return -1
	}
	mb.subsLock.Lock()
	defer mb.subsLock.Unlock()
	n := 0
	for _, m := range mb.wants {
		if m[sub] {
			n += 1
		}
	}
	return n
}

// return topics subscribed to as a slice of strings
//
// returns an empty slice if either the Mbus is
// closed or if the subscriber has no topics.
func (sub *Subr) Topics() []Topic {
	mb := sub.mbus
	if mb.closed {
		return make([]Topic, 0)
	}
	mb.subsLock.Lock()
	defer mb.subsLock.Unlock()
	rv := make([]Topic, 0, 10)
	for t, m := range mb.wants {
		if m[sub] {
			rv = append(rv, t)
		}
	}
	return rv
}

// publish a message
//
// The message is added to the publisher queue and then a
// bool is sent down the wake channel to (possibly) wake
// the bcaster goroutine.  Returns true on success, false
// otherwise, e.g. if the Mbus has already been Close()d
func (mb *Mbus) Pub(m Msg) bool {
	mb.wakeLock.Lock()
	defer mb.wakeLock.Unlock()
	if mb.closed {
		return false
	}
	// add message to publisher queue (locking happens within
	// the fifo code)
	mb.msgs.Add(PMsg(&m))
	// make sure the bcaster has an item in its wake fifo
	if len(mb.wake) == 0 {
		// no race condition (due to the lock above) so send
		// always succeeds
		mb.wake <- true
	}
	return true
}

// goroutine to distribute messages from the publish queue to
// the input queue of all interested subscribers.
//
// When the wake channel is closed, this goroutine will
// exit, possibly after draining its queue
func (mb *Mbus) bcaster() {
	for {
		if _, ok := <-mb.wake; !ok {
			break
		}
		// whenever a receive from wake succeeds, it means
		// there might be messages in the queue

		// don't allow subscriber list to change while we're
		// distributing messages
		mb.subsLock.Lock()
		for {
			m := mb.msgs.Next()
			if m == nil {
				break
			}
			msg := m.(PMsg)
			if w, ok := mb.wants[msg.Topic]; ok {
				for sub, _ := range w {
					sub.msgs.Add(msg)
					// ensure the wake channel contains an item
					// to force the carrier to wake up
					if len(sub.wake) == 0 {
						// no race condition here because
						// only a single bcaster goroutine
						// runs this code, so this send
						// always succeeds
						sub.wake <- true
					}
				}
			}
			// distribute message to any subscribers to "*", regardless of topic
			if w, ok := mb.wants["*"]; ok {
				for sub, _ := range w {
					sub.msgs.Add(msg)
					if len(sub.wake) == 0 {
						sub.wake <- true
					}
				}
			}
		}
		mb.subsLock.Unlock()
	}
}

// goroutine to send desired messages to subscriber's channel
//
// When the subscriber's wake channel is closed, this
// goroutine will close the client's message channel and
// exit, possibly after draining its queue.
func (sub *Subr) viewer() {
	for {
		if _, ok := <-sub.wake; !ok {
			break
		}
		// whenever a receive from wake succeeds, it means
		// there might be messages in the queue
		for {
			msg := sub.msgs.Next()
			if msg == nil {
				break
			}
			sub.mchn <- msg.(PMsg)
		}
	}
	close(sub.mchn)
}
