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

package kldwebhooks

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/globalsign/mgo"
	"github.com/kaleido-io/ethconnect/internal/kldkafka"
	"github.com/kaleido-io/ethconnect/internal/kldmessages"
	log "github.com/sirupsen/logrus"
)

// MongoDatabase is a subset of mgo that we use, allowing stubbing
type MongoDatabase interface {
	Connect(url string) error
	GetCollection(database string, collection string) MongoCollection
}

type mgoWrapper struct {
	session *mgo.Session
}

func (m *mgoWrapper) Connect(url string) (err error) {
	m.session, err = mgo.Dial(url)
	return
}

func (m *mgoWrapper) GetCollection(database string, collection string) MongoCollection {
	return m.session.DB(database).C(collection)
}

// MongoCollection is the subset of mgo that we use, allowing stubbing
type MongoCollection interface {
	Insert(...interface{}) error
	Create(info *mgo.CollectionInfo) error
}

func (w *WebhooksBridge) connectMongoDB(mongo MongoDatabase) (err error) {
	if w.conf.MongoDB.URL == "" {
		log.Debugf("No MongoDB URL configured. Receipt store disabled")
		return
	}
	err = mongo.Connect(w.conf.MongoDB.URL)
	if err != nil {
		err = fmt.Errorf("Unable to connect to MongoDB: %s", err)
		return
	}
	w.mongo = mongo.GetCollection(w.conf.MongoDB.Database, w.conf.MongoDB.Collection)
	if collErr := w.mongo.Create(&mgo.CollectionInfo{
		Capped:  (w.conf.MongoDB.MaxDocs > 0),
		MaxDocs: w.conf.MongoDB.MaxDocs,
	}); collErr != nil {
		log.Infof("MongoDB collection exists: %s", err)
	}
	log.Infof("Connected to MongoDB on %s DB=%s Collection=%s", w.conf.MongoDB.URL, w.conf.MongoDB.Database, w.conf.MongoDB.Collection)
	return
}

// getString is a helper to safely extract strings from generic interface maps
func getString(genericMap map[string]interface{}, key string) string {
	log.Infof("genericMap: %+v", genericMap)
	if val, exists := genericMap[key]; exists {
		if reflect.TypeOf(val).Kind() == reflect.String {
			return val.(string)
		}
	}
	return ""
}

// processReply processes an individual reply message, and contains all errors
func (w *WebhooksBridge) processReply(msgBytes []byte) {

	// Parse the reply as JSON
	var parsedMsg map[string]interface{}
	if err := json.Unmarshal(msgBytes, &parsedMsg); err != nil {
		log.Errorf("Unable to unmarshal reply message '%s' as JSON: %s", string(msgBytes), err)
		return
	}

	// Extract the headers
	var headers map[string]interface{}
	if iHeaders, exists := parsedMsg["headers"]; exists && reflect.TypeOf(headers).Kind() == reflect.Map {
		headers = iHeaders.(map[string]interface{})
	} else {
		log.Errorf("Failed to extract request headers from '%s'", string(msgBytes))
		return
	}

	// The one field we require is the original ID (as it's the key in MongoDB)
	requestId := getString(headers, "requestId")
	if requestId == "" {
		log.Errorf("Failed to extract headers.requestId from '%s'", string(msgBytes))
		return
	}
	reqOffset := getString(headers, "reqOffset")
	msgType := getString(headers, "type")
	result := ""
	if msgType == kldmessages.MsgTypeError {
		result = getString(parsedMsg, "errorMessage")
	} else {
		result = getString(parsedMsg, "transactionHash")
	}
	log.Infof("Received reply message. requestId='%s' reqOffset='%s' type='%s': %s", requestId, reqOffset, msgType, result)

	// Insert the receipt into MongoDB
	if requestId != "" && w.mongo != nil {
		parsedMsg["_id"] = requestId
		if err := w.mongo.Insert(parsedMsg); err != nil {
			log.Errorf("Failed to insert '%s' into mongodb: %s", string(msgBytes), err)
		} else {
			log.Infof("Inserted receipt into MongoDB")
		}
	}
}

// ConsumerMessagesLoop - consume replies
func (w *WebhooksBridge) ConsumerMessagesLoop(consumer kldkafka.KafkaConsumer, producer kldkafka.KafkaProducer, wg *sync.WaitGroup) {
	for msg := range consumer.Messages() {
		w.processReply(msg.Value)
	}
	wg.Done()
}
