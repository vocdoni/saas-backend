package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	ethcommon "github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func TestSignTxHandler(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.Reset(); err != nil {
			c.Logf("error resetting test database: %v", err)
		}
	}()
	// create user and organization
	userDataJson := mustMarshal(&UserInfo{
		Email:     testEmail,
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	})
	signupReq, err := http.NewRequest(http.MethodPost, testURL(usersEndpoint), bytes.NewBuffer(userDataJson))
	c.Assert(err, qt.IsNil)
	signuoResp, err := http.DefaultClient.Do(signupReq)
	c.Assert(err, qt.IsNil)
	c.Assert(signuoResp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(signuoResp.Body.Close(), qt.IsNil)
	c.Assert(signuoResp.Body.Close(), qt.IsNil)
	// get the verification code from the email
	signupMailBody, err := testMailService.FindEmail(context.Background(), testEmail)
	c.Assert(err, qt.IsNil)
	// create a regex to find the verification code in the email
	mailCodeRgx := regexp.MustCompile(fmt.Sprintf(`%s(.{%d})`, VerificationCodeTextBody, VerificationCodeLength*2))
	mailCode := mailCodeRgx.FindStringSubmatch(signupMailBody)
	// verify the user
	verifyJson := mustMarshal(&UserVerification{
		Email: testEmail,
		Code:  mailCode[1],
	})
	verifyReq, err := http.NewRequest(http.MethodPost, testURL(verifyUserEndpoint), bytes.NewBuffer(verifyJson))
	c.Assert(err, qt.IsNil)
	verifyResp, err := http.DefaultClient.Do(verifyReq)
	c.Assert(err, qt.IsNil)
	c.Assert(verifyResp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(verifyResp.Body.Close(), qt.IsNil)
	// request login
	loginReq, err := http.NewRequest(http.MethodPost, testURL(authLoginEndpoint), bytes.NewBuffer(userDataJson))
	c.Assert(err, qt.IsNil)
	loginResp, err := http.DefaultClient.Do(loginReq)
	c.Assert(err, qt.IsNil)
	c.Assert(loginResp.StatusCode, qt.Equals, http.StatusOK)
	// parse login response
	var loginRespData *LoginResponse
	c.Assert(json.NewDecoder(loginResp.Body).Decode(&loginRespData), qt.IsNil)
	c.Assert(loginResp.Body.Close(), qt.IsNil)
	// create an organization
	orgDataJson := mustMarshal(&OrganizationInfo{
		Name:        "Test Organization",
		Type:        "community",
		Description: "My amazing testing organization",
		Size:        "100",
		Color:       "#ff0000",
		Logo:        "https://placehold.co/128x128.png",
		Subdomain:   "mysubdomain",
		Language:    "ES",
		Timezone:    "GMT+2",
		Country:     "Spain",
		Header:      "https://placehold.co/600x400.png",
	})
	orgReq, err := http.NewRequest(http.MethodPost, testURL(organizationsEndpoint), bytes.NewBuffer(orgDataJson))
	c.Assert(err, qt.IsNil)
	// include the user token in the request headers
	orgReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginRespData.Token))
	orgResp, err := http.DefaultClient.Do(orgReq)
	c.Assert(err, qt.IsNil)
	c.Assert(orgResp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(orgResp.Body.Close(), qt.IsNil)
	// get the organization address
	orgsReq, err := http.NewRequest(http.MethodGet, testURL(authAddressesEndpoint), nil)
	c.Assert(err, qt.IsNil)
	orgsReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginRespData.Token))
	orgsResp, err := http.DefaultClient.Do(orgsReq)
	c.Assert(err, qt.IsNil)
	c.Assert(orgsResp.StatusCode, qt.Equals, http.StatusOK)
	var orgsAddress *OrganizationAddresses
	c.Assert(json.NewDecoder(orgsResp.Body).Decode(&orgsAddress), qt.IsNil)
	c.Assert(orgsResp.Body.Close(), qt.IsNil)
	// parse org address
	strMainOrgAddress := orgsAddress.Addresses[0]
	mainOrgAddress := ethcommon.HexToAddress(strMainOrgAddress)
	c.Run("setAccountTx", func(c *qt.C) {
		infoUri := "https://example.com"
		authHeaders := map[string]string{
			"Authorization": fmt.Sprintf("Bearer %s", loginRespData.Token),
		}
		// generate random address
		randSignKeys := ethereum.NewSignKeys()
		c.Assert(randSignKeys.Generate(), qt.IsNil)
		// different account set account tx
		differentAccountTx := &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: &models.SetAccountTx{
					Account: randSignKeys.Address().Bytes(),
					Txtype:  models.TxType_CREATE_ACCOUNT,
					InfoURI: &infoUri,
				},
			},
		}
		bDifferentAccountTx, err := proto.Marshal(differentAccountTx)
		c.Assert(err, qt.IsNil)
		// no info uri set account tx
		noInfoUriTx := &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: &models.SetAccountTx{
					Account: mainOrgAddress.Bytes(),
					Txtype:  models.TxType_CREATE_ACCOUNT,
				},
			},
		}
		bNoInfoUriTx, err := proto.Marshal(noInfoUriTx)
		c.Assert(err, qt.IsNil)
		// no account set account tx
		noAccountTx := &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: &models.SetAccountTx{
					Txtype:  models.TxType_CREATE_ACCOUNT,
					InfoURI: &infoUri,
				},
			},
		}
		bNoAccountTx, err := proto.Marshal(noAccountTx)
		c.Assert(err, qt.IsNil)
		// invalid tx type set account tx
		invalidTxTypeTx := &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: &models.SetAccountTx{
					Account: mainOrgAddress.Bytes(),
					Txtype:  models.TxType(100),
					InfoURI: &infoUri,
				},
			},
		}
		bInvalidTxTypeTx, err := proto.Marshal(invalidTxTypeTx)
		c.Assert(err, qt.IsNil)
		// valid set account tx
		validSetAccountTx := &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: &models.SetAccountTx{
					Account: mainOrgAddress.Bytes(),
					Txtype:  models.TxType_CREATE_ACCOUNT,
					InfoURI: &infoUri,
				},
			},
		}
		bValidSetAccountTx, err := proto.Marshal(validSetAccountTx)
		c.Assert(err, qt.IsNil)
		tests := []apiTestCase{
			{
				name:    "differentAccount",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&TransactionData{
					Address:   strMainOrgAddress,
					TxPayload: base64.StdEncoding.EncodeToString(bDifferentAccountTx),
				}),
				expectedBody:   mustMarshal(ErrUnauthorized.With("invalid account")),
				expectedStatus: http.StatusUnauthorized,
			},
			{
				name:    "noInfoUri",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&TransactionData{
					Address:   strMainOrgAddress,
					TxPayload: base64.StdEncoding.EncodeToString(bNoInfoUriTx),
				}),
				expectedBody:   mustMarshal(ErrInvalidTxFormat.With("missing fields")),
				expectedStatus: http.StatusBadRequest,
			},
			{
				name:    "noAccount",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&TransactionData{
					Address:   strMainOrgAddress,
					TxPayload: base64.StdEncoding.EncodeToString(bNoAccountTx),
				}),
				expectedBody:   mustMarshal(ErrInvalidTxFormat.With("missing fields")),
				expectedStatus: http.StatusBadRequest,
			},
			{
				name:    "invalidTxType",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&TransactionData{
					Address:   strMainOrgAddress,
					TxPayload: base64.StdEncoding.EncodeToString(bInvalidTxTypeTx),
				}),
				expectedBody:   mustMarshal(ErrTxTypeNotAllowed),
				expectedStatus: http.StatusBadRequest,
			},
			{
				name:    "validSetAccount",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&TransactionData{
					Address:   strMainOrgAddress,
					TxPayload: base64.StdEncoding.EncodeToString(bValidSetAccountTx),
				}),
				expectedStatus: http.StatusOK,
			},
		}
		// run the tests
		for _, tt := range tests {
			runAPITestCase(c, tt)
		}
	})
}
