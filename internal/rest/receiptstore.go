// Copyright 2019 Kaleido

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/hyperledger/firefly-ethconnect/internal/auth"
	"github.com/hyperledger/firefly-ethconnect/internal/contractgateway"
	"github.com/hyperledger/firefly-ethconnect/internal/errors"
	"github.com/hyperledger/firefly-ethconnect/internal/messages"
	"github.com/hyperledger/firefly-ethconnect/internal/receipts"
	"github.com/hyperledger/firefly-ethconnect/internal/utils"
	"github.com/julienschmidt/httprouter"
	log "github.com/sirupsen/logrus"
)

const (
	defaultReceiptLimit      = 10
	defaultRetryTimeout      = 120 * 1000
	defaultRetryInitialDelay = 500
	defaultMaxDocs           = 250
	backoffFactor            = 1.1
)

var uuidCharsVerifier, _ = regexp.Compile("^[0-9a-zA-Z-]+$")

type receiptStore struct {
	conf            *receipts.ReceiptStoreConf
	persistence     receipts.ReceiptStorePersistence
	smartContractGW contractgateway.SmartContractGateway
	reservedIDs     map[string]bool
	reservationMux  sync.Mutex
}

func newReceiptStore(conf *receipts.ReceiptStoreConf, persistence receipts.ReceiptStorePersistence, smartContractGW contractgateway.SmartContractGateway) *receiptStore {
	if conf.RetryTimeoutMS <= 0 {
		conf.RetryTimeoutMS = defaultRetryTimeout
	}
	if conf.RetryInitialDelayMS <= 0 {
		conf.RetryInitialDelayMS = defaultRetryInitialDelay
	}
	if conf.MaxDocs <= 0 {
		conf.MaxDocs = defaultMaxDocs
	}
	return &receiptStore{
		conf:            conf,
		persistence:     persistence,
		smartContractGW: smartContractGW,
		reservedIDs:     make(map[string]bool),
	}
}

func (r *receiptStore) addRoutes(router *httprouter.Router) {
	router.GET("/replies", r.getReplies)
	router.GET("/replies/:id", r.getReply)
	router.GET("/reply/:id", r.getReply)
}

func (r *receiptStore) extractHeaders(parsedMsg map[string]interface{}) map[string]interface{} {
	if iHeaders, exists := parsedMsg["headers"]; exists {
		if headers, ok := iHeaders.(map[string]interface{}); ok {
			return headers
		}
	}
	return nil
}

func (r *receiptStore) reserveID(msgID string) (release func(), err error) {

	r.reservationMux.Lock()
	defer r.reservationMux.Unlock()

	m, err := r.persistence.GetReceipt(msgID)
	if err != nil {
		return nil, err
	}
	if m != nil || r.reservedIDs[msgID] {
		return nil, errors.Errorf(errors.ReceiptStoreKeyNotUnique)
	}

	r.reservedIDs[msgID] = true
	return func() {
		r.reservationMux.Lock()
		defer r.reservationMux.Unlock()

		delete(r.reservedIDs, msgID)
	}, nil

}

func (r *receiptStore) writeAccepted(msgID, msgAck string, msg map[string]interface{}) error {
	msg["receivedAt"] = time.Now().UnixNano() / int64(time.Millisecond)
	msg["pending"] = true
	msg["msgAck"] = msgAck
	msg["_id"] = msgID
	return r.writeReceipt(msgID, msg, false)
}

func (r *receiptStore) processReply(msgBytes []byte) {

	// Parse the reply as JSON
	var parsedMsg map[string]interface{}
	if err := json.Unmarshal(msgBytes, &parsedMsg); err != nil {
		log.Errorf("Unable to unmarshal reply message '%s' as JSON: %s", string(msgBytes), err)
		return
	}

	// Extract the headers
	headers := r.extractHeaders(parsedMsg)
	if headers == nil {
		log.Errorf("Failed to extract request headers from '%+v'", parsedMsg)
		return
	}

	// The one field we require is the original ID (as it's the key in MongoDB)
	requestID := utils.GetMapString(headers, "requestId")
	if requestID == "" {
		log.Errorf("Failed to extract headers.requestId from '%+v'", parsedMsg)
		return
	}
	reqOffset := utils.GetMapString(headers, "reqOffset")
	msgType := utils.GetMapString(headers, "type")
	contractAddr := utils.GetMapString(parsedMsg, "contractAddress")
	result := ""
	switch msgType {
	case messages.MsgTypeError:
		result = utils.GetMapString(parsedMsg, "errorMessage")
	case messages.MsgTypeTransactionRedeliveryPrevented:
		// If we receive this, then we need to make sure either:
		// a) We have a good receipt in our DB already
		// b) We swap the status into an error - as the application might have to check the transaction status themselves from the TX Hash
		result = utils.GetMapString(parsedMsg, "transactionHash")
		existingReceipt, err := r.persistence.GetReceipt(requestID)
		if err == nil && existingReceipt != nil {
			existingHeaders := r.extractHeaders(*existingReceipt)
			msgType := utils.GetMapString(existingHeaders, "type")
			if msgType == messages.MsgTypeTransactionFailure || msgType == messages.MsgTypeTransactionSuccess {
				// We already have a valid receipt - do not overwrite it
				log.Warnf("Ignoring redelivery reply message. requestId='%s' reqOffset='%s' type='%s': %s", requestID, reqOffset, msgType, result)
				return
			}
		}
		// We need to switch to an error to let them know we cannot provide the receipt
		idempotencyErr := errors.Errorf(errors.ResubmissionPreventedCheckTransactionHash)
		parsedMsg["errorCode"] = idempotencyErr.Code()
		parsedMsg["errorMessage"] = idempotencyErr.ErrorNoCode()
	default:
		result = utils.GetMapString(parsedMsg, "transactionHash")
	}
	log.Infof("Received reply message. requestId='%s' reqOffset='%s' type='%s': %s", requestID, reqOffset, msgType, result)

	if r.smartContractGW != nil && msgType == messages.MsgTypeTransactionSuccess && contractAddr != "" {
		var receipt messages.TransactionReceipt
		if err := json.Unmarshal(msgBytes, &receipt); err == nil {
			if err = r.smartContractGW.PostDeploy(&receipt); err != nil {
				log.Errorf("Failed to process receipt in smart contract gateway: %s", err)
			}
		} else {
			log.Errorf("Failed to parse message as transaction receipt: %s", err)
		}
	}

	parsedMsg["receivedAt"] = time.Now().UnixNano() / int64(time.Millisecond)
	parsedMsg["_id"] = requestID

	// Insert the receipt into persistence - performs retry for errors, so will succeed or panic
	if requestID != "" && r.persistence != nil {
		_ = r.writeReceipt(requestID, parsedMsg, true /* overwrite, and succeed or panic */)
	}

}

func (r *receiptStore) writeReceipt(requestID string, receipt map[string]interface{}, overwriteAndRetry bool) error {
	startTime := time.Now()
	delay := time.Duration(r.conf.RetryInitialDelayMS) * time.Millisecond
	attempt := 0
	retryTimeout := time.Duration(r.conf.RetryTimeoutMS) * time.Millisecond

	for {
		if attempt > 0 {
			log.Infof("%s: Waiting %.2fs before re-attempt:%d mongo write", requestID, delay.Seconds(), attempt)
			time.Sleep(delay)
			delay = time.Duration(float64(delay) * backoffFactor)
		}
		attempt++
		err := r.persistence.AddReceipt(requestID, &receipt, overwriteAndRetry)
		if err == nil {
			log.Infof("%s: Inserted receipt into receipt store", receipt["_id"])
			break
		}

		if !overwriteAndRetry {
			return err
		}

		log.Errorf("%s: addReceipt attempt: %d failed, err: %s", requestID, attempt, err)

		timeRetrying := time.Since(startTime)
		if timeRetrying > retryTimeout {
			log.Infof("%s: receipt: %+v", requestID, receipt)
			log.Panicf("%s: Failed to insert into receipt store after %.2fs: %s", requestID, timeRetrying.Seconds(), err)
		}
	}
	if r.smartContractGW != nil {
		r.smartContractGW.SendReply(receipt)
	}
	return nil
}

func (r *receiptStore) marshalAndReply(res http.ResponseWriter, req *http.Request, result interface{}) {
	// Serialize and return
	resBytes, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Errorf("Error serializing receipts: %s", err)
		sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreSerializeResponse), 500)
		return
	}
	status := 200
	log.Infof("<-- %s %s [%d]", req.Method, req.URL, status)
	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(status)
	_, _ = res.Write(resBytes)
}

// getReplies handles a HTTP request for recent replies
func (r *receiptStore) getReplies(res http.ResponseWriter, req *http.Request, params httprouter.Params) {
	log.Infof("--> %s %s", req.Method, req.URL)

	err := auth.AuthListAsyncReplies(req.Context())
	if err != nil {
		log.Errorf("Error querying replies: %s", err)
		sendRESTError(res, req, errors.Errorf(errors.Unauthorized), 401)
		return
	}

	res.Header().Set("Content-Type", "application/json")
	if r.persistence == nil {
		sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreDisabled), 405)
		return
	}

	// Default limit - which is set to zero (infinite) if we have specific IDs being request
	limit := defaultReceiptLimit
	_ = req.ParseForm()
	ids, ok := req.Form["id"]
	if ok {
		limit = 0 // can be explicitly set below, but no imposed limit when we have a list of IDs
		for idx, id := range ids {
			if !uuidCharsVerifier.MatchString(id) {
				log.Errorf("Invalid id '%s' %d", id, idx)
				sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreInvalidRequestID), 400)
				return
			}
		}
	}

	// Extract limit
	limitStr := req.FormValue("limit")
	if limitStr != "" {
		if customLimit, err := strconv.ParseInt(limitStr, 10, 32); err == nil {
			if int(customLimit) > r.conf.QueryLimit {
				log.Errorf("Invalid limit value: %s", err)
				sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreInvalidRequestMaxLimit, r.conf.QueryLimit), 400)
				return
			} else if customLimit > 0 {
				limit = int(customLimit)
			}
		} else {
			log.Errorf("Invalid limit value: %s", err)
			sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreInvalidRequestBadLimit), 400)
			return
		}
	}

	// Extract skip
	var skip int
	skipStr := req.FormValue("skip")
	if skipStr != "" {
		if skipI64, err := strconv.ParseInt(skipStr, 10, 32); err == nil && skipI64 > 0 {
			skip = int(skipI64)
		} else {
			log.Errorf("Invalid skip value: %s", err)
			sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreInvalidRequestBadSkip), 400)
			return
		}
	}

	// Verify since - if specified
	var sinceEpochMS int64
	since := req.FormValue("since")
	if since != "" {
		if isoTime, err := time.Parse(time.RFC3339Nano, since); err == nil {
			sinceEpochMS = isoTime.UnixNano() / int64(time.Millisecond)
		} else {
			if sinceEpochMS, err = strconv.ParseInt(since, 10, 64); err != nil {
				log.Errorf("since '%s' cannot be parsed as RFC3339 or millisecond timestamp: %s", since, err)
				sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreInvalidRequestBadSince), 400)
			}
		}
	}

	from := req.FormValue("from")
	to := req.FormValue("to")
	start := req.FormValue("start")

	// Call the persistence tier - which must return an empty array when no results (not an error)
	results, err := r.persistence.GetReceipts(skip, limit, ids, sinceEpochMS, from, to, start)
	if err != nil {
		log.Errorf("Error querying replies: %s", err)
		sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreFailedQuery, err), 500)
		return
	}
	log.Debugf("Replies query: skip=%d limit=%d replies=%d", skip, limit, len(*results))
	r.marshalAndReply(res, req, results)

}

// getReply handles a HTTP request for an individual reply
func (r *receiptStore) getReply(res http.ResponseWriter, req *http.Request, params httprouter.Params) {
	log.Infof("--> %s %s", req.Method, req.URL)

	err := auth.AuthReadAsyncReplyByUUID(req.Context())
	if err != nil {
		log.Errorf("Error querying reply: %s", err)
		sendRESTError(res, req, errors.Errorf(errors.Unauthorized), 401)
		return
	}

	requestID := params.ByName("id")
	// Call the persistence tier - which must return an empty array when no results (not an error)
	result, err := r.persistence.GetReceipt(requestID)
	if err != nil {
		log.Errorf("Error querying reply: %s", err)
		sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreFailedQuerySingle, err), 500)
		return
	} else if result == nil {
		sendRESTError(res, req, errors.Errorf(errors.ReceiptStoreFailedNotFound), 404)
		log.Infof("Reply not found")
		return
	}
	log.Infof("Reply found")
	r.marshalAndReply(res, req, result)
}
