package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	ethcommon "github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

const (
	// VerificationCodeLength is the length of the verification code in bytes
	VerificationCodeLength = 3
	// VerificationCodeTextBody is the body of the verification code email
	VerificationCodeTextBody = "Your Vocdoni verification code is: "
)

func TestSignTxHandler(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.Reset(); err != nil {
			c.Logf("error resetting test database: %v", err)
		}
	}()
	// create user and organization
	userDataJSON := mustMarshal(&apicommon.UserInfo{
		Email:     testEmail,
		Password:  testPass,
		FirstName: testFirstName,
		LastName:  testLastName,
	})
	signupReq, err := http.NewRequest(http.MethodPost, testURL(usersEndpoint), bytes.NewBuffer(userDataJSON))
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
	verifyJSON := mustMarshal(&apicommon.UserVerification{
		Email: testEmail,
		Code:  mailCode[1],
	})
	verifyReq, err := http.NewRequest(http.MethodPost, testURL(verifyUserEndpoint), bytes.NewBuffer(verifyJSON))
	c.Assert(err, qt.IsNil)
	verifyResp, err := http.DefaultClient.Do(verifyReq)
	c.Assert(err, qt.IsNil)
	c.Assert(verifyResp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(verifyResp.Body.Close(), qt.IsNil)
	// request login
	loginReq, err := http.NewRequest(http.MethodPost, testURL(authLoginEndpoint), bytes.NewBuffer(userDataJSON))
	c.Assert(err, qt.IsNil)
	loginResp, err := http.DefaultClient.Do(loginReq)
	c.Assert(err, qt.IsNil)
	c.Assert(loginResp.StatusCode, qt.Equals, http.StatusOK)
	// parse login response
	var loginRespData *apicommon.LoginResponse
	c.Assert(json.NewDecoder(loginResp.Body).Decode(&loginRespData), qt.IsNil)
	c.Assert(loginResp.Body.Close(), qt.IsNil)
	// create an organization
	orgDataJSON := mustMarshal(&apicommon.OrganizationInfo{
		Type:      "community",
		Size:      "100",
		Color:     "#ff0000",
		Subdomain: "mysubdomain",
		Timezone:  "GMT+2",
		Country:   "Spain",
	})
	orgReq, err := http.NewRequest(http.MethodPost, testURL(organizationsEndpoint), bytes.NewBuffer(orgDataJSON))
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
	var orgsAddress *apicommon.OrganizationAddresses
	c.Assert(json.NewDecoder(orgsResp.Body).Decode(&orgsAddress), qt.IsNil)
	c.Assert(orgsResp.Body.Close(), qt.IsNil)
	// parse org address
	strMainOrgAddress := orgsAddress.Addresses[0]
	mainOrgAddress := ethcommon.HexToAddress(strMainOrgAddress)
	c.Run("setAccountTx", func(c *qt.C) {
		infoURI := "https://example.com"
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
					InfoURI: &infoURI,
				},
			},
		}
		bDifferentAccountTx, err := proto.Marshal(differentAccountTx)
		c.Assert(err, qt.IsNil)
		// no info uri set account tx
		noInfoURITx := &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: &models.SetAccountTx{
					Account: mainOrgAddress.Bytes(),
					Txtype:  models.TxType_CREATE_ACCOUNT,
				},
			},
		}
		bNoInfoURITx, err := proto.Marshal(noInfoURITx)
		c.Assert(err, qt.IsNil)
		// no account set account tx
		noAccountTx := &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: &models.SetAccountTx{
					Txtype:  models.TxType_CREATE_ACCOUNT,
					InfoURI: &infoURI,
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
					InfoURI: &infoURI,
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
					InfoURI: &infoURI,
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
				body: mustMarshal(&apicommon.TransactionData{
					Address:   mainOrgAddress.Bytes(),
					TxPayload: bDifferentAccountTx,
				}),
				expectedBody:   mustMarshal(errors.ErrUnauthorized.With("invalid account")),
				expectedStatus: http.StatusUnauthorized,
			},
			{
				name:    "noInfoURI",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&apicommon.TransactionData{
					Address:   mainOrgAddress.Bytes(),
					TxPayload: (bNoInfoURITx),
				}),
				expectedBody:   mustMarshal(errors.ErrInvalidTxFormat.With("missing fields")),
				expectedStatus: http.StatusBadRequest,
			},
			{
				name:    "noAccount",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&apicommon.TransactionData{
					Address:   mainOrgAddress.Bytes(),
					TxPayload: (bNoAccountTx),
				}),
				expectedBody:   mustMarshal(errors.ErrInvalidTxFormat.With("missing fields")),
				expectedStatus: http.StatusBadRequest,
			},
			{
				name:    "invalidTxType",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&apicommon.TransactionData{
					Address:   mainOrgAddress.Bytes(),
					TxPayload: bInvalidTxTypeTx,
				}),
				expectedBody:   mustMarshal(errors.ErrInvalidTxFormat.With("invalid SetAccount tx type")),
				expectedStatus: http.StatusBadRequest,
			},
			{
				name:    "validSetAccount",
				uri:     testURL(signTxEndpoint),
				method:  http.MethodPost,
				headers: authHeaders,
				body: mustMarshal(&apicommon.TransactionData{
					Address:   mainOrgAddress.Bytes(),
					TxPayload: bValidSetAccountTx,
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
