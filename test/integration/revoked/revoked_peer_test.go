/*
Copyright SecureKey Technologies Inc. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package revoked

import (
	"path"
	"testing"

	"github.com/hyperledger/fabric-sdk-go/pkg/common/errors/retry"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/logging"
	contextAPI "github.com/hyperledger/fabric-sdk-go/pkg/common/providers/context"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/core"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/fab"
	"github.com/hyperledger/fabric-sdk-go/pkg/common/providers/msp"

	packager "github.com/hyperledger/fabric-sdk-go/pkg/fab/ccpackager/gopackager"
	"github.com/hyperledger/fabric-sdk-go/pkg/fabsdk"

	"github.com/hyperledger/fabric-sdk-go/pkg/client/resmgmt"
	"github.com/hyperledger/fabric-sdk-go/test/integration"
	"github.com/hyperledger/fabric-sdk-go/test/metadata"

	"github.com/hyperledger/fabric-sdk-go/pkg/client/channel"
	"github.com/hyperledger/fabric-sdk-go/pkg/core/config"
	"github.com/hyperledger/fabric-sdk-go/pkg/core/config/lookup"
	"github.com/hyperledger/fabric-sdk-go/pkg/core/mocks"
	"github.com/hyperledger/fabric-sdk-go/third_party/github.com/hyperledger/fabric/common/cauthdsl"
	"github.com/stretchr/testify/assert"
)

const (
	org1             = "Org1"
	org2             = "Org2"
	ordererAdminUser = "Admin"
	ordererOrgName   = "ordererorg"
	org1AdminUser    = "Admin"
	org2AdminUser    = "Admin"
	org1User         = "User1"
	channelID        = "orgchannel"
	configPath       = "../../fixtures/config/config_test.yaml"
)

var logger = logging.NewLogger("fabsdk/test")

// Peers used for testing
var orgTestPeer0 fab.Peer
var orgTestPeer1 fab.Peer

// TestRevokedPeer
func TestRevokedPeer(t *testing.T) {
	// Create SDK setup for the integration tests with revoked peer
	sdk, err := fabsdk.New(getConfigBackend(t))
	if err != nil {
		t.Fatalf("Failed to create new SDK: %s", err)
	}
	defer sdk.Close()

	// Delete all private keys from the crypto suite store
	// and users from the user store at the end
	integration.CleanupUserData(t, sdk)
	defer integration.CleanupUserData(t, sdk)

	//prepare contexts
	ordererClientContext := sdk.Context(fabsdk.WithUser(ordererAdminUser), fabsdk.WithOrg(ordererOrgName))
	org1AdminClientContext := sdk.Context(fabsdk.WithUser(org1AdminUser), fabsdk.WithOrg(org1))
	org2AdminClientContext := sdk.Context(fabsdk.WithUser(org2AdminUser), fabsdk.WithOrg(org2))
	org1ChannelClientContext := sdk.ChannelContext(channelID, fabsdk.WithUser(org1User), fabsdk.WithOrg(org1))

	// Channel management client is responsible for managing channels (create/update channel)
	chMgmtClient, err := resmgmt.New(ordererClientContext)
	if err != nil {
		t.Fatal(err)
	}

	// Get signing identity that is used to sign create channel request
	org1AdminUser, err := integration.GetSigningIdentity(sdk, org1AdminUser, org1)
	if err != nil {
		t.Fatalf("failed to get org1AdminUser, err : %v", err)
	}

	org2AdminUser, err := integration.GetSigningIdentity(sdk, org2AdminUser, org2)
	if err != nil {
		t.Fatalf("failed to get org2AdminUser, err : %v", err)
	}

	req := resmgmt.SaveChannelRequest{ChannelID: "orgchannel",
		ChannelConfigPath: path.Join("../../../", metadata.ChannelConfigPath, "orgchannel.tx"),
		SigningIdentities: []msp.SigningIdentity{org1AdminUser, org2AdminUser}}
	txID, err := chMgmtClient.SaveChannel(req, resmgmt.WithRetry(retry.DefaultResMgmtOpts))
	assert.Nil(t, err, "error should be nil")
	assert.NotEmpty(t, txID, "transaction ID should be populated")

	// Org1 resource management client (Org1 is default org)
	org1ResMgmt, err := resmgmt.New(org1AdminClientContext)
	if err != nil {
		t.Fatalf("Failed to create new resource management client: %s", err)
	}

	// Org1 peers join channel
	if err = org1ResMgmt.JoinChannel("orgchannel", resmgmt.WithRetry(retry.DefaultResMgmtOpts)); err != nil {
		t.Fatalf("Org1 peers failed to JoinChannel: %s", err)
	}

	// Org2 resource management client
	org2ResMgmt, err := resmgmt.New(org2AdminClientContext)
	if err != nil {
		t.Fatal(err)
	}

	// Org2 peers join channel
	if err = org2ResMgmt.JoinChannel("orgchannel", resmgmt.WithRetry(retry.DefaultResMgmtOpts)); err != nil {
		t.Fatalf("Org2 peers failed to JoinChannel: %s", err)
	}

	// Create chaincode package for example cc
	ccPkg, err := packager.NewCCPackage("github.com/example_cc", "../../fixtures/testdata")
	if err != nil {
		t.Fatal(err)
	}

	installCCReq := resmgmt.InstallCCRequest{Name: "exampleCC", Path: "github.com/example_cc", Version: "0", Package: ccPkg}

	// Install example cc to Org1 peers
	_, err = org1ResMgmt.InstallCC(installCCReq)
	if err != nil {
		t.Fatal(err)
	}

	// Install example cc to Org2 peers
	_, err = org2ResMgmt.InstallCC(installCCReq, resmgmt.WithRetry(retry.DefaultResMgmtOpts))
	if err != nil {
		t.Fatal(err)
	}

	// Set up chaincode policy to 'any of two msps'
	ccPolicy := cauthdsl.SignedByAnyMember([]string{"Org1MSP", "Org2MSP"})

	// Org1 resource manager will instantiate 'example_cc' on 'orgchannel'
	resp, err := org1ResMgmt.InstantiateCC("orgchannel",
		resmgmt.InstantiateCCRequest{Name: "exampleCC", Path: "github.com/example_cc", Version: "0", Args: integration.ExampleCCInitArgs(), Policy: ccPolicy},
		resmgmt.WithTargetURLs("peer0.org1.example.com"))
	assert.Nil(t, err, "error should be nil")
	assert.NotEmpty(t, resp, "transaction response should be populated")

	// Load specific targets for move funds test - one of the
	//targets has its certificate revoked
	loadOrgPeers(t, org1AdminClientContext)

	// Org1 user connects to 'orgchannel'
	chClientOrg1User, err := channel.New(org1ChannelClientContext)
	if err != nil {
		t.Fatalf("Failed to create new channel client for Org1 user: %s", err)
	}

	// Org1 user queries initial value on both peers
	// Since one of the peers on channel has certificate revoked, eror is expected here
	// Error in container is :
	// .... identity 0 does not satisfy principal:
	// Could not validate identity against certification chain, err The certificate has been revoked
	_, err = chClientOrg1User.Query(channel.Request{ChaincodeID: "exampleCC", Fcn: "invoke", Args: integration.ExampleCCQueryArgs()})
	if err == nil {
		t.Fatalf("Expected error: '....Description: could not find chaincode with name 'exampleCC',,, ")
	}

}

func loadOrgPeers(t *testing.T, ctxProvider contextAPI.ClientProvider) {

	ctx, err := ctxProvider()
	if err != nil {
		t.Fatalf("context creation failed: %s", err)
	}

	org1Peers, err := ctx.EndpointConfig().PeersConfig(org1)
	if err != nil {
		t.Fatal(err)
	}

	org2Peers, err := ctx.EndpointConfig().PeersConfig(org2)
	if err != nil {
		t.Fatal(err)
	}

	orgTestPeer0, err = ctx.InfraProvider().CreatePeerFromConfig(&fab.NetworkPeer{PeerConfig: org1Peers[0]})
	if err != nil {
		t.Fatal(err)
	}

	orgTestPeer1, err = ctx.InfraProvider().CreatePeerFromConfig(&fab.NetworkPeer{PeerConfig: org2Peers[0]})
	if err != nil {
		t.Fatal(err)
	}

}

func getConfigBackend(t *testing.T) core.ConfigProvider {

	return func() (core.ConfigBackend, error) {
		backend, err := config.FromFile(configPath)()
		if err != nil {
			t.Fatalf("failed to read config backend from file, %v", err)
		}
		backendMap := make(map[string]interface{})

		networkConfig := fab.NetworkConfig{}
		//get valid peer config
		err = lookup.New(backend).UnmarshalKey("peers", &networkConfig.Peers)
		if err != nil {
			t.Fatalf("failed to unmarshal peer network config, %v", err)
		}

		//customize peer0.org2 to peer1.org2
		peer2 := networkConfig.Peers["local.peer0.org2.example.com"]
		peer2.URL = "peer1.org2.example.com:9051"
		peer2.EventURL = ""
		peer2.GRPCOptions["ssl-target-name-override"] = "peer1.org2.example.com"

		//remove peer0.org2
		delete(networkConfig.Peers, "local.peer0.org2.example.com")

		//add peer1.org2
		networkConfig.Peers["local.peer1.org2.example.com"] = peer2

		//get valid org2
		err = lookup.New(backend).UnmarshalKey("organizations", &networkConfig.Organizations)
		if err != nil {
			t.Fatalf("failed to unmarshal organizations network config, %v", err)
		}

		//Customize org2
		org2 := networkConfig.Organizations["org2"]
		org2.Peers = []string{"peer1.org2.example.com"}
		org2.MSPID = "Org2MSP"
		networkConfig.Organizations["org2"] = org2

		//custom channel
		err = lookup.New(backend).UnmarshalKey("channels", &networkConfig.Channels)
		if err != nil {
			t.Fatalf("failed to unmarshal entityMatchers network config, %v", err)
		}

		orgChannel := networkConfig.Channels[channelID]
		delete(orgChannel.Peers, "peer0.org2.example.com")
		orgChannel.Peers["peer1.org2.example.com"] = fab.PeerChannelConfig{
			EndorsingPeer:  true,
			ChaincodeQuery: true,
			LedgerQuery:    true,
			EventSource:    false,
		}
		networkConfig.Channels[channelID] = orgChannel

		//custom entity matchers
		err = lookup.New(backend).UnmarshalKey("entityMatchers", &networkConfig.EntityMatchers)
		if err != nil {
			t.Fatalf("failed to unmarshal entityMatchers network config, %v", err)
		}

		peerEntityMatchers := networkConfig.EntityMatchers["peer"]
		newMatch := fab.MatchConfig{
			Pattern:                             "peer1.org2.example.com",
			URLSubstitutionExp:                  "peer1.org2.example.com:9051",
			EventURLSubstitutionExp:             "",
			SSLTargetOverrideURLSubstitutionExp: "",
			MappedHost:                          "local.peer1.org2.example.com",
		}
		peerEntityMatchers = append([]fab.MatchConfig{newMatch}, peerEntityMatchers...)
		networkConfig.EntityMatchers["peer"] = peerEntityMatchers

		//Customize backend with update peers, organizations, channels and entity matchers config
		backendMap["peers"] = networkConfig.Peers
		backendMap["organizations"] = networkConfig.Organizations
		backendMap["channels"] = networkConfig.Channels
		backendMap["entityMatchers"] = networkConfig.EntityMatchers

		return &mocks.MockConfigBackend{KeyValueMap: backendMap, CustomBackend: backend}, nil
	}
}
