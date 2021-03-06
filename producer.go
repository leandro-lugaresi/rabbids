package rabbids

import (
	"fmt"
	"sync"
	"time"

	"github.com/leveeml/rabbids/serialization"
	retry "github.com/rafaeljesus/retry-go"
	"github.com/streadway/amqp"
)

// Producer is an high level rabbitMQ producer instance.
type Producer struct {
	mutex         sync.RWMutex
	conf          Connection
	conn          *amqp.Connection
	ch            *amqp.Channel
	closed        chan struct{}
	emit          chan Publishing
	emitErr       chan PublishingError
	notifyClose   chan *amqp.Error
	log           LoggerFN
	serializer    Serializer
	declarations  *declarations
	exDeclared    map[string]struct{}
	delayDelivery *delayDelivery
	name          string
}

// NewProcucer create a new high level rabbitMQ producer instance
//
// dsn is a string in the AMQP URI format
// the ProducerOptions can be:
//   rabbids.WithLogger     - to set a logger instance
//   rabbids.WithFactory    - to use one instance of a factory.
//                            when added the factory is used to declare the topics
//                            in the first time the topic is used.
//   rabbids.WithSerializer - used to set a specific serializer
//                            the default is the a JSON serializer.
func NewProducer(dsn string, opts ...ProducerOption) (*Producer, error) {
	p := &Producer{
		conf: Connection{
			DSN:     dsn,
			Retries: DefaultRetries,
			Sleep:   DefaultSleep,
			Timeout: DefaultTimeout,
		},
		emit:          make(chan Publishing, 250),
		emitErr:       make(chan PublishingError, 250),
		closed:        make(chan struct{}),
		log:           NoOPLoggerFN,
		serializer:    &serialization.JSON{},
		exDeclared:    make(map[string]struct{}),
		delayDelivery: &delayDelivery{},
		name:          fmt.Sprintf("rabbids.producer.%d", time.Now().Unix()),
	}

	for _, opt := range opts {
		if err := opt(p); err != nil {
			return nil, err
		}
	}

	err := p.startConnection()
	if err != nil {
		return nil, err
	}

	go p.loop()

	return p, nil
}

// the internal loop to handle signals from rabbitMQ and the async api.
func (p *Producer) loop() {
	for {
		select {
		case err := <-p.notifyClose:
			if err == nil {
				return // graceful shutdown?
			}

			p.handleAMPQClose(err)
		case pub, ok := <-p.emit:
			if !ok {
				p.closed <- struct{}{}
				return // graceful shutdown
			}

			err := p.Send(pub)
			if err != nil {
				p.tryToEmitErr(pub, err)
			}
		}
	}
}

// Emit emits a message to rabbitMQ but does not wait for the response from the broker.
// Errors with the Publishing (encoding, validation) or with the broker will be sent to the EmitErr channel.
// It's your responsibility to handle these errors somehow.
func (p *Producer) Emit() chan<- Publishing { return p.emit }

// EmitErr returns a channel used to receive all the errors from Emit channel.
// The error handle is not required but and the send inside this channel is buffered.
// WARNING: If the channel gets full, new errors will be dropped to avoid stop the producer internal loop.
func (p *Producer) EmitErr() <-chan PublishingError { return p.emitErr }

// Send a message to rabbitMQ.
// In case of connection errors, the send will block and retry until the reconnection is done.
// It returns an error if the Serializer returned an error OR the connection error persisted after the retries.
func (p *Producer) Send(m Publishing) error {
	for _, op := range m.options {
		op(&m)
	}

	b, err := p.serializer.Marshal(m.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal: %w", err)
	}

	m.Body = b
	m.ContentType = p.serializer.Name()

	if m.Delay > 0 {
		err := p.delayDelivery.Declare(p.ch, m.Key)
		if err != nil {
			return err
		}
	}

	return retry.Do(func() error {
		p.mutex.RLock()
		p.tryToDeclareTopic(m.Exchange)

		err := p.ch.Publish(m.Exchange, m.Key, false, false, m.Publishing)
		p.mutex.RUnlock()

		return err
	}, 10, 10*time.Millisecond)
}

// Close will close all the underline channels and close the connection with rabbitMQ.
// Any Emit call after calling the Close method will panic.
func (p *Producer) Close() error {
	close(p.emit)
	<-p.closed

	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.ch != nil && p.conn != nil && !p.conn.IsClosed() {
		if err := p.ch.Close(); err != nil {
			return fmt.Errorf("error closing the channel: %w", err)
		}

		if err := p.conn.Close(); err != nil {
			return fmt.Errorf("error closing the connection: %w", err)
		}
	}

	close(p.emitErr)

	return nil
}

// GetAMQPChannel returns the current connection channel.
func (p *Producer) GetAMQPChannel() *amqp.Channel {
	return p.ch
}

// GetAGetAMQPConnection returns the current amqp connetion.
func (p *Producer) GetAMQPConnection() *amqp.Connection {
	return p.conn
}

func (p *Producer) handleAMPQClose(err error) {
	p.log("ampq connection closed", Fields{"error": err})

	for {
		connErr := p.startConnection()
		if connErr == nil {
			return
		}

		p.log("ampq reconnection failed", Fields{"error": connErr})
		time.Sleep(time.Second)
	}
}

func (p *Producer) startConnection() error {
	p.log("opening a new rabbitmq connection", Fields{})

	conn, err := openConnection(p.conf, p.name)
	if err != nil {
		return err
	}

	p.mutex.Lock()

	p.conn = conn
	p.ch, err = p.conn.Channel()
	p.notifyClose = p.conn.NotifyClose(make(chan *amqp.Error))

	p.mutex.Unlock()

	return err
}

func (p *Producer) tryToEmitErr(m Publishing, err error) {
	data := PublishingError{Publishing: m, Err: err}
	select {
	case p.emitErr <- data:
	default:
	}
}

func (p *Producer) tryToDeclareTopic(ex string) {
	if p.declarations == nil || ex == "" {
		return
	}

	if _, ok := p.exDeclared[ex]; !ok {
		err := p.declarations.declareExchange(p.ch, ex)
		if err != nil {
			p.log("failed declaring a exchange", Fields{"err": err, "ex": ex})
			return
		}

		p.exDeclared[ex] = struct{}{}
	}
}
