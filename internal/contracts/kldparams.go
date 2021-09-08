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

package contracts

import (
	"net/http"
	"net/textproto"
	"strings"

	"github.com/hyperledger-labs/firefly-ethconnect/internal/utils"
)

func getQueryParamNoCase(name string, req *http.Request) []string {
	name = strings.ToLower(name)
	req.ParseForm()
	for k, vs := range req.Form {
		if strings.ToLower(k) == name {
			return vs
		}
	}
	return nil
}

// getFlyParam standardizes how special 'fly' params are specified, in query params, or headers
func getFlyParam(name string, req *http.Request) string {
	valStr := ""
	vs := getQueryParamNoCase(utils.GetenvOrDefaultLowerCase("PREFIX_SHORT", "fly")+"-"+name, req)
	if len(vs) > 0 {
		valStr = vs[0]
	}
	if valStr == "" {
		valStr = req.Header.Get("x-" + utils.GetenvOrDefaultLowerCase("PREFIX_LONG", "firefly") + "-" + name)
	}
	return valStr
}

// getFlyParamBool returns a 'fly' param as a boolean
func getFlyParamBool(name string, req *http.Request) bool {
	valStr := ""
	vs := getQueryParamNoCase(utils.GetenvOrDefaultLowerCase("PREFIX_SHORT", "fly")+"-"+name, req)
	if len(vs) == 0 {
		valStr = req.Header.Get("x-" + utils.GetenvOrDefaultLowerCase("PREFIX_LONG", "firefly") + "-" + name)
	} else {
		valStr = vs[0]
		if valStr == "" {
			valStr = "true"
		}
	}
	return strings.ToLower(valStr) == "true"
}

// getFlyParamMulti returns an array parameter, or nil if none specified.
// allows multiple query params / headers, or a single comma-separated query param / header
func getFlyParamMulti(name string, req *http.Request) (val []string) {
	req.ParseForm()
	val = getQueryParamNoCase(utils.GetenvOrDefaultLowerCase("PREFIX_SHORT", "fly")+"-"+name, req)
	if len(val) == 0 {
		val = textproto.MIMEHeader(req.Header)[textproto.CanonicalMIMEHeaderKey("x-"+utils.GetenvOrDefaultLowerCase("PREFIX_LONG", "firefly")+"-"+name)]
	}
	if len(val) == 1 {
		val = strings.Split(val[0], ",")
	}
	return
}
