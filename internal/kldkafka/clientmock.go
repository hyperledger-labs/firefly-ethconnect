// Copyright 2018 Kaleido, a ConsenSys business

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kldkafka

import (
	"github.com/Shopify/sarama"
	cluster "github.com/bsm/sarama-cluster"
)

// MockKafkaFactory - mock
type MockKafkaFactory struct {
	ClientConf         *cluster.Config
	ErrorOnNewClient   error
	ErrorOnNewProducer error
	ErrorOnNewConsumer error
	Producer           *MockKafkaProducer
	Consumer           *MockKafkaConsumer
}

// NewMockKafkaFactory - mock
func NewMockKafkaFactory() *MockKafkaFactory {
	return &MockKafkaFactory{}
}

// NewErrorMockKafkaFactory - mock
func NewErrorMockKafkaFactory(errorOnNewClient error, errorOnNewConsumer error, errorOnNewProducer error) *MockKafkaFactory {
	f := NewMockKafkaFactory()
	f.ErrorOnNewClient = errorOnNewClient
	f.ErrorOnNewConsumer = errorOnNewConsumer
	f.ErrorOnNewProducer = errorOnNewProducer
	return f
}

// NewClient - mock
func (f *MockKafkaFactory) NewClient(k KafkaCommon, clientConf *cluster.Config) (KafkaClient, error) {
	f.ClientConf = clientConf
	return f, f.ErrorOnNewClient
}

// Brokers - mock
func (f *MockKafkaFactory) Brokers() []*sarama.Broker {
	return []*sarama.Broker{
		&sarama.Broker{},
	}
}

// NewProducer - mock
func (f *MockKafkaFactory) NewProducer(k KafkaCommon) (KafkaProducer, error) {
	f.Producer = &MockKafkaProducer{
		MockInput:     make(chan *sarama.ProducerMessage),
		MockSuccesses: make(chan *sarama.ProducerMessage),
		MockErrors:    make(chan *sarama.ProducerError),
	}
	return f.Producer, f.ErrorOnNewProducer
}

// NewConsumer - mock
func (f *MockKafkaFactory) NewConsumer(k KafkaCommon) (KafkaConsumer, error) {
	f.Consumer = &MockKafkaConsumer{
		MockMessages:       make(chan *sarama.ConsumerMessage),
		MockNotifications:  make(chan *cluster.Notification),
		MockErrors:         make(chan error),
		OffsetsByPartition: make(map[int32]int64),
	}
	return f.Consumer, f.ErrorOnNewConsumer
}

// MockKafkaProducer - mock
type MockKafkaProducer struct {
	MockInput     chan *sarama.ProducerMessage
	MockSuccesses chan *sarama.ProducerMessage
	MockErrors    chan *sarama.ProducerError
}

// AsyncClose - mock
func (p *MockKafkaProducer) AsyncClose() {
	if p.MockInput != nil {
		close(p.MockInput)
	}
	if p.MockSuccesses != nil {
		close(p.MockSuccesses)
	}
	if p.MockErrors != nil {
		close(p.MockErrors)
	}
}

// Input - mock
func (p *MockKafkaProducer) Input() chan<- *sarama.ProducerMessage {
	return p.MockInput
}

// Successes - mock
func (p *MockKafkaProducer) Successes() <-chan *sarama.ProducerMessage {
	return p.MockSuccesses
}

// Errors - mock
func (p *MockKafkaProducer) Errors() <-chan *sarama.ProducerError {
	return p.MockErrors
}

// MockKafkaConsumer - mock
type MockKafkaConsumer struct {
	MockMessages       chan *sarama.ConsumerMessage
	MockNotifications  chan *cluster.Notification
	MockErrors         chan error
	OffsetsByPartition map[int32]int64
}

// Close - mock
func (c *MockKafkaConsumer) Close() error {
	if c.MockMessages != nil {
		close(c.MockMessages)
	}
	if c.MockNotifications != nil {
		close(c.MockNotifications)
	}
	if c.MockErrors != nil {
		close(c.MockErrors)
	}
	return nil
}

// Messages - mock
func (c *MockKafkaConsumer) Messages() <-chan *sarama.ConsumerMessage {
	return c.MockMessages
}

// Notifications - mock
func (c *MockKafkaConsumer) Notifications() <-chan *cluster.Notification {
	return c.MockNotifications
}

// Errors - mock
func (c *MockKafkaConsumer) Errors() <-chan error {
	return c.MockErrors
}

// MarkOffset - mock
func (c *MockKafkaConsumer) MarkOffset(msg *sarama.ConsumerMessage, metadata string) {
	c.OffsetsByPartition[msg.Partition] = msg.Offset
	return
}
