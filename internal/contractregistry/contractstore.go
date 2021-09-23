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

package contractregistry

import (
	"bufio"
	"encoding/json"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-openapi/spec"
	lru "github.com/hashicorp/golang-lru"
	log "github.com/sirupsen/logrus"

	ethconnecterrors "github.com/hyperledger/firefly-ethconnect/internal/errors"
	"github.com/hyperledger/firefly-ethconnect/internal/messages"
)

const (
	// DefaultABICacheSize is the number of entries we will hold in a LRU cache for ABIs
	DefaultABICacheSize = 25
)

type ContractResolver interface {
	ResolveContractAddress(registeredName string) (string, error)
	GetContractByAddress(addrHex string) (*ContractInfo, error)
	GetABI(location ABILocation, refresh bool) (deployMsg *DeployContractWithAddress, err error)
	CheckNameAvailable(name string, isRemote bool) error
}

type ContractStore interface {
	ContractResolver
	Init() error
	Close()
	AddContract(addrHexNo0x, abiID, pathName, registerAs string) (*ContractInfo, error)
	AddABI(id string, deployMsg *messages.DeployContract, createdTime time.Time) *ABIInfo
	AddRemoteInstance(lookupStr, address string) error
	GetLocalABIInfo(abiID string) (*ABIInfo, error)
	ListContracts() []messages.TimeSortable
	ListABIs() []messages.TimeSortable
}

type ContractStoreConf struct {
	StoragePath string `json:"storagePath"`
	BaseURL     string `json:"baseURL"`
}

type contractStore struct {
	conf                  *ContractStoreConf
	rr                    RemoteRegistry
	contractIndex         map[string]messages.TimeSortable
	contractRegistrations map[string]*ContractInfo
	idxLock               sync.Mutex
	abiIndex              map[string]messages.TimeSortable
	abiCache              *lru.Cache
}

func NewContractStore(conf *ContractStoreConf, rr RemoteRegistry) ContractStore {
	return &contractStore{
		conf:                  conf,
		rr:                    rr,
		contractIndex:         make(map[string]messages.TimeSortable),
		contractRegistrations: make(map[string]*ContractInfo),
		abiIndex:              make(map[string]messages.TimeSortable),
	}
}

// ContractInfo is the minimal data structure we keep in memory, indexed by address
// ONLY used for local registry. Remote registry handles its own storage/caching
type ContractInfo struct {
	messages.TimeSorted
	Address      string `json:"address"`
	Path         string `json:"path"`
	ABI          string `json:"abi"`
	SwaggerURL   string `json:"openapi"`
	RegisteredAs string `json:"registeredAs"`
}

// ABIInfo is the minimal data structure we keep in memory, indexed by our own UUID
type ABIInfo struct {
	messages.TimeSorted
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Path            string `json:"path"`
	Deployable      bool   `json:"deployable"`
	SwaggerURL      string `json:"openapi"`
	CompilerVersion string `json:"compilerVersion"`
}

func (i *ContractInfo) GetID() string {
	return i.Address
}

func (i *ABIInfo) GetID() string {
	return i.ID
}

type abiType int

// These values will be saved with existing subscriptions.
// Do not remove or reorder them (only add new ones to the end).
const (
	RemoteGateway abiType = iota
	RemoteInstance
	LocalABI
)

type ABILocation struct {
	ABIType abiType `json:"type"`
	Name    string  `json:"name"`
}

func IsRemote(msg messages.CommonHeaders) bool {
	ctxMap := msg.Context
	if isRemoteGeneric, ok := ctxMap[RemoteRegistryContextKey]; ok {
		if isRemote, ok := isRemoteGeneric.(bool); ok {
			return isRemote
		}
	}
	return false
}

func (cs *contractStore) AddContract(addrHexNo0x, abiID, pathName, registerAs string) (*ContractInfo, error) {
	contractInfo := &ContractInfo{
		Address:      addrHexNo0x,
		ABI:          abiID,
		Path:         "/contracts/" + pathName,
		SwaggerURL:   cs.conf.BaseURL + "/contracts/" + pathName + "?swagger",
		RegisteredAs: registerAs,
		TimeSorted: messages.TimeSorted{
			CreatedISO8601: time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := cs.storeContractInfo(contractInfo); err != nil {
		return nil, err
	}
	return contractInfo, nil
}

func (cs *contractStore) storeContractInfo(info *ContractInfo) error {
	if err := cs.addToContractIndex(info); err != nil {
		return err
	}
	infoFile := path.Join(cs.conf.StoragePath, "contract_"+info.Address+".instance.json")
	instanceBytes, _ := json.MarshalIndent(info, "", "  ")
	log.Infof("%s: Storing contract instance JSON to '%s'", info.ABI, infoFile)
	if err := ioutil.WriteFile(infoFile, instanceBytes, 0664); err != nil {
		return ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayLocalStoreContractSave, err)
	}
	return nil
}

func (cs *contractStore) ResolveContractAddress(registeredName string) (string, error) {
	nameUnescaped, _ := url.QueryUnescape(registeredName)
	info, exists := cs.contractRegistrations[nameUnescaped]
	if !exists {
		return "", ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayLocalStoreContractLoad, registeredName)
	}
	log.Infof("%s -> 0x%s", registeredName, info.Address)
	return info.Address, nil
}

func (cs *contractStore) GetContractByAddress(addrHex string) (*ContractInfo, error) {
	addrHexNo0x := strings.TrimPrefix(strings.ToLower(addrHex), "0x")
	info, exists := cs.contractIndex[addrHexNo0x]
	if !exists {
		return nil, ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayLocalStoreContractNotFound, addrHexNo0x)
	}
	return info.(*ContractInfo), nil
}

func (cs *contractStore) GetABI(location ABILocation, refresh bool) (deployMsg *DeployContractWithAddress, err error) {
	if !refresh {
		if cached, ok := cs.abiCache.Get(location); ok {
			result := cached.(*DeployContractWithAddress)
			return result, nil
		}
	}

	switch location.ABIType {
	case RemoteGateway:
		deployMsg = &DeployContractWithAddress{}
		deployMsg.Contract, err = cs.rr.LoadFactoryForGateway(location.Name, refresh)
	case RemoteInstance:
		deployMsg, err = cs.rr.LoadFactoryForInstance(location.Name, refresh)
	case LocalABI:
		deployMsg = &DeployContractWithAddress{}
		deployMsg.Contract, _, err = cs.getLocalABI(location.Name)
	default:
		panic("unknown ABI type") // should not happen
	}

	if err != nil || deployMsg == nil || deployMsg.Contract == nil {
		return nil, err
	}
	cs.abiCache.Add(location, deployMsg)
	return deployMsg, nil
}

func (cs *contractStore) getLocalABI(abiID string) (*messages.DeployContract, *ABIInfo, error) {
	info, err := cs.GetLocalABIInfo(abiID)
	if err != nil {
		return nil, nil, err
	}

	msg, err := cs.loadDeployMsg(abiID)
	if err != nil || msg == nil {
		return nil, nil, err
	}

	return msg, info, nil
}

func (cs *contractStore) GetLocalABIInfo(abiID string) (*ABIInfo, error) {
	ts, exists := cs.abiIndex[abiID]
	if !exists {
		log.Infof("ABI with ID %s not found locally", abiID)
		return nil, ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayLocalStoreABINotFound, abiID)
	}
	return ts.(*ABIInfo), nil
}

func (cs *contractStore) loadDeployMsg(abiID string) (*messages.DeployContract, error) {
	deployFile := path.Join(cs.conf.StoragePath, "abi_"+abiID+".deploy.json")
	deployBytes, err := ioutil.ReadFile(deployFile)
	if err != nil {
		return nil, ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayLocalStoreABILoad, abiID, err)
	}
	msg := &messages.DeployContract{}
	if err = json.Unmarshal(deployBytes, msg); err != nil {
		return nil, ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayLocalStoreABIParse, abiID, err)
	}
	return msg, nil
}

func (cs *contractStore) buildIndex() {
	log.Infof("Building installed smart contract index")
	legacyContractMatcher, _ := regexp.Compile(`^contract_([0-9a-z]{40})\.swagger\.json$`)
	instanceMatcher, _ := regexp.Compile(`^contract_([0-9a-z]{40})\.instance\.json$`)
	abiMatcher, _ := regexp.Compile(`^abi_([0-9a-z-]+)\.deploy.json$`)
	files, err := ioutil.ReadDir(cs.conf.StoragePath)
	if err != nil {
		log.Errorf("Failed to read directory %s: %s", cs.conf.StoragePath, err)
		return
	}
	for _, file := range files {
		fileName := file.Name()
		legacyContractGroups := legacyContractMatcher.FindStringSubmatch(fileName)
		abiGroups := abiMatcher.FindStringSubmatch(fileName)
		instanceGroups := instanceMatcher.FindStringSubmatch(fileName)
		if legacyContractGroups != nil {
			cs.migrateLegacyContract(legacyContractGroups[1], path.Join(cs.conf.StoragePath, fileName), file.ModTime())
		} else if instanceGroups != nil {
			cs.addFileToContractIndex(instanceGroups[1], path.Join(cs.conf.StoragePath, fileName))
		} else if abiGroups != nil {
			cs.addFileToABIIndex(abiGroups[1], path.Join(cs.conf.StoragePath, fileName), file.ModTime())
		}
	}
	log.Infof("Smart contract index built. %d entries", len(cs.contractIndex))
}

func (cs *contractStore) Init() (err error) {
	if cs.abiCache, err = lru.New(DefaultABICacheSize); err != nil {
		return ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayResourceErr, err)
	}
	cs.buildIndex()
	return cs.rr.Init()
}

func (cs *contractStore) Close() {
	cs.rr.Close()
}

func (cs *contractStore) migrateLegacyContract(address, fileName string, createdTime time.Time) {
	swaggerFile, err := os.OpenFile(fileName, os.O_RDONLY, 0)
	if err != nil {
		log.Errorf("Failed to load Swagger file %s: %s", fileName, err)
		return
	}
	defer swaggerFile.Close()
	var swagger spec.Swagger
	err = json.NewDecoder(bufio.NewReader(swaggerFile)).Decode(&swagger)
	if err != nil {
		log.Errorf("Failed to parse Swagger file %s: %s", fileName, err)
		return
	}
	if swagger.Info == nil {
		log.Errorf("Failed to migrate invalid Swagger file %s", fileName)
		return
	}
	var registeredAs string
	if ext, exists := swagger.Info.Extensions["x-firefly-registered-name"]; exists {
		registeredAs = ext.(string)
	}
	if ext, exists := swagger.Info.Extensions["x-firefly-deployment-id"]; exists {
		_, err := cs.AddContract(address, ext.(string), address, registeredAs)
		if err != nil {
			log.Errorf("Failed to write migrated instance file: %s", err)
			return
		}

		if err := os.Remove(fileName); err != nil {
			log.Errorf("Failed to clean-up migrated file %s: %s", fileName, err)
		}

	} else {
		log.Warnf("Swagger cannot be migrated due to missing 'x-firefly-deployment-id' extension: %s", fileName)
	}

}

func (cs *contractStore) addFileToContractIndex(address, fileName string) {
	contractFile, err := os.OpenFile(fileName, os.O_RDONLY, 0)
	if err != nil {
		log.Errorf("Failed to load contract instance file %s: %s", fileName, err)
		return
	}
	defer contractFile.Close()
	var contractInfo ContractInfo
	err = json.NewDecoder(bufio.NewReader(contractFile)).Decode(&contractInfo)
	if err != nil {
		log.Errorf("Failed to parse contract instance deployment file %s: %s", fileName, err)
		return
	}
	err = cs.addToContractIndex(&contractInfo)
	if err != nil {
		log.Errorf("Failed to add to contract index %s: %s", fileName, err)
	}
}

func (cs *contractStore) addFileToABIIndex(id, fileName string, createdTime time.Time) {
	deployFile, err := os.OpenFile(fileName, os.O_RDONLY, 0)
	if err != nil {
		log.Errorf("Failed to load ABI deployment file %s: %s", fileName, err)
		return
	}
	defer deployFile.Close()
	var deployMsg messages.DeployContract
	err = json.NewDecoder(bufio.NewReader(deployFile)).Decode(&deployMsg)
	if err != nil {
		log.Errorf("Failed to parse ABI deployment file %s: %s", fileName, err)
		return
	}
	cs.AddABI(id, &deployMsg, createdTime)
}

func (cs *contractStore) CheckNameAvailable(registerAs string, isRemote bool) error {
	if isRemote {
		msg, err := cs.rr.LoadFactoryForInstance(registerAs, false)
		if err != nil {
			return err
		} else if msg != nil {
			return ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayFriendlyNameClash, msg.Address, registerAs)
		}
		return nil
	}
	if existing, exists := cs.contractRegistrations[registerAs]; exists {
		return ethconnecterrors.Errorf(ethconnecterrors.RESTGatewayFriendlyNameClash, existing.Address, registerAs)
	}
	return nil
}

func (cs *contractStore) addToContractIndex(info *ContractInfo) error {
	cs.idxLock.Lock()
	defer cs.idxLock.Unlock()
	if info.RegisteredAs != "" {
		// Protect against overwrite
		if err := cs.CheckNameAvailable(info.RegisteredAs, false); err != nil {
			return err
		}
		log.Infof("Registering %s as '%s'", info.Address, info.RegisteredAs)
		cs.contractRegistrations[info.RegisteredAs] = info
	}
	cs.contractIndex[info.Address] = info
	return nil
}

func (cs *contractStore) AddABI(id string, deployMsg *messages.DeployContract, createdTime time.Time) *ABIInfo {
	cs.idxLock.Lock()
	info := &ABIInfo{
		ID:              id,
		Name:            deployMsg.ContractName,
		Description:     deployMsg.Description,
		Deployable:      len(deployMsg.Compiled) > 0,
		CompilerVersion: deployMsg.CompilerVersion,
		Path:            "/abis/" + id,
		SwaggerURL:      cs.conf.BaseURL + "/abis/" + id + "?swagger",
		TimeSorted: messages.TimeSorted{
			CreatedISO8601: createdTime.UTC().Format(time.RFC3339),
		},
	}
	cs.abiIndex[id] = info
	cs.idxLock.Unlock()
	return info
}

func (cs *contractStore) AddRemoteInstance(lookupStr, address string) error {
	return cs.rr.RegisterInstance(lookupStr, address)
}

func (cs *contractStore) ListContracts() []messages.TimeSortable {
	cs.idxLock.Lock()
	retval := make([]messages.TimeSortable, 0, len(cs.contractIndex))
	for _, info := range cs.contractIndex {
		retval = append(retval, info)
	}
	cs.idxLock.Unlock()

	// Do the sort by Title then Address
	sort.Slice(retval, func(i, j int) bool {
		return retval[i].IsLessThan(retval[i], retval[j])
	})
	return retval
}

func (cs *contractStore) ListABIs() []messages.TimeSortable {
	cs.idxLock.Lock()
	retval := make([]messages.TimeSortable, 0, len(cs.abiIndex))
	for _, info := range cs.abiIndex {
		retval = append(retval, info)
	}
	cs.idxLock.Unlock()

	// Do the sort by Title then Address
	sort.Slice(retval, func(i, j int) bool {
		return retval[i].IsLessThan(retval[i], retval[j])
	})
	return retval
}
