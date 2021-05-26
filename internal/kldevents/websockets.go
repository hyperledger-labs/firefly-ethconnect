// Copyright 2020 Kaleido

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kldevents

import (
	"github.com/kaleido-io/ethconnect/internal/klderrors"
)

type webSocketAction struct {
	es   *eventStream
	spec *webSocketActionInfo
}

func newWebSocketAction(es *eventStream, spec *webSocketActionInfo) (*webSocketAction, error) {
	if es.wsChannels == nil {
		return nil, klderrors.Errorf(klderrors.EventStreamsWebSocketNotConfigured)
	}
	return &webSocketAction{
		es:   es,
		spec: spec,
	}, nil
}

// attemptBatch attempts to deliver a batch over socket IO
func (w *webSocketAction) attemptBatch(batchNumber, attempt uint64, events []*eventData) error {
	var err error

	// Implicitly use a topic of "" if no topic has been set
	topic := ""
	if w.spec != nil {
		topic = w.spec.Topic
	}

	// Get a blocking channel to send and receive on our chosen namespace
	sender, broadcaster, receiver, closing := w.es.wsChannels.GetChannels(topic)

	var channel chan<- interface{}
	switch w.spec.DistributionMode {
	case DistributionModeBroadcast:
		channel = broadcaster
	default:
		channel = sender
	}

	// Sent the batch of events
	select {
	case channel <- events:
		break
	case <-w.es.updateInterrupt:
		return klderrors.Errorf(klderrors.EventStreamsWebSocketInterruptedSend)
	case <-closing:
		return klderrors.Errorf(klderrors.EventStreamsWebSocketInterruptedSend)
	}

	// If we ever add more distribution modes, we may want to change this logic from a simple if statement
	if w.spec.DistributionMode != DistributionModeBroadcast {
		// Wait for the next ack or exception
		select {
		case err = <-receiver:
			break
		case <-w.es.updateInterrupt:
			return klderrors.Errorf(klderrors.EventStreamsWebSocketInterruptedReceive)
		case <-closing:
			return klderrors.Errorf(klderrors.EventStreamsWebSocketInterruptedReceive)
		}
		// Pass back any exception from the client
	}
	return err
}
