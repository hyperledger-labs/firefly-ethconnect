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
	"sync"

	"github.com/Shopify/sarama"
	cluster "github.com/bsm/sarama-cluster"
)

// KafkaGoRoutines defines goroutines for processing Kafka messages from KafkaCommon
type KafkaGoRoutines interface {
	ConsumerMessagesLoop(consumer KafkaConsumer, producer KafkaProducer, wg *sync.WaitGroup)
	ProducerErrorLoop(consumer KafkaConsumer, producer KafkaProducer, wg *sync.WaitGroup)
	ProducerSuccessLoop(consumer KafkaConsumer, producer KafkaProducer, wg *sync.WaitGroup)
}

// KafkaProducer provides the interface passed from KafkaCommon to produce messages (subset of sarama)
type KafkaProducer interface {
	AsyncClose()
	Input() chan<- *sarama.ProducerMessage
	Successes() <-chan *sarama.ProducerMessage
	Errors() <-chan *sarama.ProducerError
}

// KafkaConsumer provides the interface passed from KafkaCommon to consume messages (subset of sarama-cluster)
type KafkaConsumer interface {
	Close() error
	Messages() <-chan *sarama.ConsumerMessage
	Notifications() <-chan *cluster.Notification
	Errors() <-chan error
	MarkOffset(*sarama.ConsumerMessage, string)
}

// KafkaFactory builds new clients
type KafkaFactory interface {
	NewClient(KafkaCommon, *cluster.Config) (KafkaClient, error)
}

// KafkaClient is the kafka client
type KafkaClient interface {
	NewProducer(KafkaCommon) (KafkaProducer, error)
	NewConsumer(KafkaCommon) (KafkaConsumer, error)
	Brokers() []*sarama.Broker
}

// SaramaKafkaFactory - uses sarama and sarama-cluster
type SaramaKafkaFactory struct{}

// NewClient - returns a new sarama-cluster client
func (f *SaramaKafkaFactory) NewClient(k KafkaCommon, clientConf *cluster.Config) (c KafkaClient, err error) {
	var client *cluster.Client
	if client, err = cluster.NewClient(k.Conf().Brokers, clientConf); err == nil {
		c = &saramaKafkaClient{client: client}
	}
	return
}

type saramaKafkaClient struct {
	client *cluster.Client
}

func (c *saramaKafkaClient) Brokers() []*sarama.Broker {
	return c.client.Brokers()
}

func (c *saramaKafkaClient) NewProducer(k KafkaCommon) (KafkaProducer, error) {
	return sarama.NewAsyncProducerFromClient(c.client.Client)
}

func (c *saramaKafkaClient) NewConsumer(k KafkaCommon) (KafkaConsumer, error) {
	return cluster.NewConsumerFromClient(c.client, k.Conf().ConsumerGroup, []string{k.Conf().TopicIn})
}
