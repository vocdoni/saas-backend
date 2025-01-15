# API Docs

<details>
  <summary>Table of contents</summary>
  <br/>

- [🔐 Auth](#-auth)
  - [🔑 Login](#-login)
  - [🥤 Refresh token](#-refresh-token)
  - [💼 User writable organizations addresses](#-user-writable-organizations-addresses)
- [🧾 Transactions](#-transactions)
  - [✍️ Sign tx](#-sign-tx)
  - [📝 Sign message](#-sign-message)
- [👥 Users](#-users)
  - [🙋 Register](#-register)
  - [✅ Verify user](#-verify-user)
  - [🪪 User verification code info](#-user-verification-code-info)
  - [📤 Resend user verification code](#-resend-user-verification-code)
  - [🧑‍💻 Get current user info](#-get-current-user-info)
  - [💇 Update current user info](#-update-current-user-info)
  - [🔏 Update current user password](#-update-current-user-password)
  - [⛓️‍💥 Request a password recovery](#%EF%B8%8F-request-a-password-recovery)
  - [🔗 Reset user password](#-reset-user-password)
- [🏤 Organizations](#-organizations)
  - [🆕 Create organization](#-create-organization)
  - [⚙️ Update organization](#-update-organization)
  - [🔍 Organization info](#-organization-info)
  - [🧑‍🤝‍🧑 Organization members](#-organization-members)
  - [🧑‍💼 Invite organization member](#-invite-organization-member)
  - [⏳ List pending invitations](#-list-pending-invitations)
  - [🤝 Accept organization invitation](#-accept-organization-invitation)
  - [💸 Organization Subscription Info](#-organization-subscription-info)
  - [🤠 Available organization members roles](#-available-organization-members-roles)
  - [🏛️ Available organization types](#-available-organization-types)
- [🏦 Plans](#-plans)
  - [📋 Get Available Plans](#-get-plans)
  - [📄 Get Plan Info](#-get-plan-info)
- [🔰 Subscriptions](#-subscriptions)
  - [🛒 Create Checkout session](#-create-checkout-session)
  - [🛍️ Get Checkout session info](#-get-checkout-session-info)
  - [🔗 Create Subscription Portal Session](#-create-subscription-portal-session)
- [📦 Storage](#-storage)
  - [ 🌄 Upload image](#-upload-image)
  - [ 📄 Get object](#-get-object)
  

</details>

## 🔐 Auth

### 🔑 Login

> **SDK method**: This method is required by the Vocdoni SDK to use this service as a valid remote signer.

* **Path** `/auth/login`
* **Method** `POST`
* **Request Body** 
```json
{
    "email": "my@email.me",
    "password": "secretpass1234"
}
```

* **Response**
```json
{
  "token": "<jwt_token>",
  "expirity": "2024-08-21T11:26:54.368718+02:00"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `401` | `40014` | `user account not verified` |
| `500` | `50002` | `internal server error` |

### 🥤 Refresh token

> **SDK method**: This method is required by the Vocdoni SDK to use this service as a valid remote signer.

* **Path** `/auth/refresh`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `500` | `50002` | `internal server error` |

### 💼 User writable organizations addresses

> **SDK method**: This method is required by the Vocdoni SDK to use this service as a valid remote signer.

This endpoint only returns the addresses of the organizations where the current user (identified by the JWT) has a role with write permission.

* **Path** `/auth/addresses`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "addresses": [
    "0x0000000001",
    "0x0000000002",
    "0x0000000003",
  ]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `404` | `40012` | `this user has not been assigned to any organization` |
| `500` | `50002` | `internal server error` |

## 🧾 Transactions

### ✍️ Sign tx

> **SDK method**: This method is required by the Vocdoni SDK to use this service as a valid remote signer.

* **Path** `/transactions`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "address": "0x...",
  "txPayload": "<base64_encoded_protobuf>"
}
```

* **Response**
```json
{
  "txPayload": "<base64_encoded_protobuf>"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40006` | `could not sign transaction` |
| `400` | `40007` | `invalid transaction format` |
| `400` | `40008` | `transaction type not allowed` |
| `500` | `50002` | `internal server error` |
| `500` | `50003` | `could not create faucet package` |

### 📝 Sign message

> **SDK method**: This method is required by the Vocdoni SDK to use this service as a valid remote signer.

* **Path** `/transactions/message`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "address": "0x...",
  "payload": "<payload_to_sign>"
}
```

* **Response**
```json
{
  "payload": "<payload_to_sign>"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |

## 👥 Users

### 🙋 Register

* **Path** `/users`
* **Method** `POST`
* **Request body**
```json
{
    "email": "my@email.me",
    "firstName": "Steve",
    "lastName": "Urkel",
    "password": "secretpass1234"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40002` | `email malformed` |
| `400` | `40003` | `password too short` |
| `400` | `40004` | `malformed JSON body` |
| `409` | `40901` | `duplicate conflict` |
| `500` | `50002` | `internal server error` |

### ✅ Verify user

* **Path** `/users/verify`
* **Method** `POST`
* **Request Body** 
```json
{
  "email": "user2veryfy@email.com",
  "code": "******",
}
```

* **Response**
```json
{
  "token": "<jwt_token>",
  "expirity": "2024-08-21T11:26:54.368718+02:00"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invalid user data` |
| `400` | `40015` | `user account already verified` |
| `401` | `40016` | `verification code expired` |
| `500` | `50002` | `internal server error` |

### 🪪 User verification code info

* **Path** `/users/verify/code`
* **Method** `GET`
* **Query params**
  * `email` 

* **Response**
```json
{
  "email": "user@email.com",
  "expiration": "2024-09-20T09:02:26.849Z",
  "valid": true
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40005` | `invalid user data` |
| `400` | `40015` | `user account already verified` |
| `404` | `40018` | `user not found` |
| `500` | `50002` | `internal server error` |

### 📤 Resend user verification code

* **Path** `/users/verify/code`
* **Method** `POST`
* **Request Body** 
```json
{
  "email": "user@email.com",
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invalid user data` |
| `400` | `40015` | `user account already verified` |
| `400` | `40017` | `last verification code still valid` |
| `500` | `50002` | `internal server error` |

### 🧑‍💻 Get current user info

* **Path** `/users/me`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "email": "test@test.test",
  "firstName": "Steve",
  "lastName": "Urkel",
  "organizations": [
    {
      "role": "admin",
      "organization": {
        "address": "0x...",
        "name": "Test Organization",
        "type": "community",
        "description": "My amazing testing organization",
        "size": 10,
        "color": "#ff0000",
        "logo": "https://[...].png",
        "subdomain": "mysubdomain",
        "timezone": "GMT+2",
        "active": true,
        "parent": {
            "...": "..."
        },
        "subscription":{
            "PlanID":3,
            "StartDate":"2024-11-07T15:25:49.218Z",
            "RenewalDate":"2025-11-07T15:25:49.218Z",
            "Active":true,
            "MaxCensusSize":10
        },
      }
    }
  ]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `500` | `50002` | `internal server error` |

### 💇 Update current user info

* **Path** `/users/me`
* **Method** `PUT`
* **Request body**
```json
{
    "email": "my@email.me",
    "firstName": "Steve",
    "lastName": "Urkel",
}
```

* **Response**

This method invalidates any previous JWT token for the user, so it returns a new token to be used in following requests.

```json
{
  "token": "<jwt_token>",
  "expirity": "2024-08-21T11:26:54.368718+02:00"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40002` | `email malformed` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |

### 🔏 Update current user password

* **Path** `/users/password`
* **Method** `PUT`
* **Request body**
```json
{
  "oldPassword": "secretpass1234",
  "newPassword": "secretpass0987"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40003` | `password too short` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |

### ⛓️‍💥 Request a password recovery

* **Path** `/users/password/recovery`
* **Method** `POST`
* **Request body**
```json
{
  "email": "user@test.com",
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |

### 🔗 Reset user password

* **Path** `/users/password/reset`
* **Method** `POST`
* **Request body**
```json
{
  "email": "user@test.com",
  "code": "******",
  "newPassword": "newpassword123"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40003` | `password too short` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |

## 🏤 Organizations

### 🆕 Create organization

* **Path** `/organizations`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Request body**
```json
{
  "name": "Test Organization",
  "website": "https://[...].com",
  "type": "community",
  "description": "My amazing testing organization",
  "size": "10",
  "color": "#ff0000",
  "logo": "https://[...].png",
  "header": "https://[...].png",
  "subdomain": "mysubdomain",
  "country": "Germany",
  "timezone": "GMT+2",
  "language": "EN",
  "communication": true
}
```
By default, the organization is created with `activated: true`.

If the user want to create a sub org, the address of the root organization must be provided inside an organization object in `parent` param. The creator must be admin of the parent organization to be able to create suborganizations. Example:
```json
{
    "parent": {
        "address": "0x..."
    }
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40009` | `organization not found` |
| `400` | `40013` | `invalid organization data` |
| `500` | `50002` | `internal server error` |

### ⚙️ Update organization

* **Path** `/organizations/{address}`
* **Method** `PUT`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Request body**
Only the following parameters can be changed. Every parameter is optional.
```json
{
  "name": "Test Organization",
  "website": "https://[...].com",
  "type": "community",
  "description": "My amazing testing organization",
  "size": "10",
  "color": "#ff0000",
  "logo": "https://[...].png",
  "header": "https://[...].png",
  "subdomain": "mysubdomain",
  "country": "Germany",
  "timezone": "GMT+2",
  "Language": "EN",
  "active": true,
  "communication": false
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40009` | `organization not found` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 🔍 Organization info

* **Path** `/organizations/{address}`
* **Method** `GET`
* **Response**
```json
{
  "address": "0x1234",
  "name": "Test Organization",
  "website": "https://[...].com",
  "type": "community",
  "description": "My amazing testing organization",
  "size": "10",
  "color": "#ff0000",
  "logo": "https://[...].png",
  "header": "https://[...].png",
  "subdomain": "mysubdomain",
  "country": "Germany",
  "timezone": "GMT+2",
  "Language": "EN",
  "active": true,
  "communications": true,
  "parent": {
    "...": "..."
  }
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40009` | `organization not found` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `4012` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 🧑‍🤝‍🧑 Organization members

* **Path** `/organizations/{address}/members`
* **Method** `GET`
* **Response**
```json
{
  "members": [
    {
      "info": { /* user info response */ },
      "role": "admin"
    }
  ]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40009` | `organization not found` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `4012` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 🧑‍💼 Invite organization member

* **Path** `/organizations/{address}/members`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request**
```json
{
  "role": "admin",
  "email": "newadmin@email.com"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40002` | `email malformed` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invalid user data` |
| `400` | `40009` | `organization not found` |
| `400` | `40011` | `no organization provided` |
| `401` | `40014` | `user account not verified` |
| `400` | `40019` | `inviation code expired` |
| `409` | `40901` | `duplicate conflict` |
| `500` | `50002` | `internal server error` |

### ⏳ List pending invitations

* **Path** `/organizations/{address}/members/pending`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Response**
```json
{
  "pending": [
    {
      "email": "newuser@email.me",
      "role": "admin",
      "expiration": "2024-12-12T12:00:00.000Z"
    }
  ]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40009` | `organization not found` |
| `400` | `40011` | `no organization provided` |
| `401` | `40014` | `user account not verified` |
| `500` | `50002` | `internal server error` |

### 🤝 Accept organization invitation

* **Path** `/organizations/{address}/members/accept`
* **Method** `POST`
* **Request**
```json
{
  "code": "a3f3b5",
  "user": { // only if the invited user is not already registered
    "firstName": "Steve",
    "lastName": "Urkel",
    "password": "secretpass1234"
  }
}
```
`user` object is only required if invited user is not registered yet.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40002` | `email malformed` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invalid user data` |
| `400` | `40009` | `organization not found` |
| `400` | `40011` | `no organization provided` |
| `401` | `40014` | `user account not verified` |
| `400` | `40019` | `inviation code expired` |
| `409` | `40901` | `duplicate conflict` |
| `500` | `50002` | `internal server error` |

### 💸 Organization subscription info

* **Path** `/organizations/{address}/subscription`
* **Method** `GET`
* **Request**
```json
{
  "subscriptionDetails":{
    "planID":3,
    "startDate":"2024-11-07T15:25:49.218Z",
    "endDate":"0001-01-01T00:00:00Z",
    "renewalDate":"0001-01-01T00:00:00Z",
    "lastPaymentDate":"0001-01-01T00:00:00Z",
    "active":true,
    "email": "test@test.com",
    "maxCensusSize":10
  },
  "usage":{
    "sentSMS":0,
    "sentEmails":0,
    "subOrgs":0,
    "members":0
  },
  "plan":{
    "id":3,
    "name":"free",
    "stripeID":"stripe_789",
    "default":true,
    "organization":{
      "memberships":10,
      "subOrgs":5,
      "censusSize":10
    },
    "votingTypes":{
      "approval":false,
      "ranked":false,
      "weighted":true
    },
    "features":{
      "personalization":false,
      "emailReminder":false,
      "smsNotification":false
    }
  }
}
```
This request can be made only by organization admins.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40009` | `organization not found` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 🤠 Available organization members roles
* **Path** `/organizations/roles`
* **Method** `GET`
* **Response**
```json
{
  "roles": [
    {
      "role": "manager",
      "name": "Manager",
      "writePermission": true
    },
    {
      "role": "viewer",
      "name": "Viewer",
      "writePermission": false
    },
    {
      "role": "admin",
      "name": "Admin",
      "writePermission": true
    }
  ]
}
```

### 🏛️ Available organization types
* **Path** `/organizations/types`
* **Method** `GET`
* **Response**
```json
{
  "types": [
    {
      "type": "cooperative",
      "name": "Cooperative"
    },
    {
      "type": "educational",
      "name": "University / Educational Institution"
    },
    {
      "type": "others",
      "name": "Others"
    },
    {
      "type": "assembly",
      "name": "Assembly"
    },
    {
      "type": "religious",
      "name": "Church / Religious Organization"
    },
    {
      "type": "company",
      "name": "Company / Corporation"
    },
    {
      "type": "political_party",
      "name": "Political Party"
    },
    {
      "type": "chamber",
      "name": "Chamber"
    },
    {
      "type": "nonprofit",
      "name": "Nonprofit / NGO"
    },
    {
      "type": "community",
      "name": "Community Group"
    },
    {
      "type": "professional_college",
      "name": "Professional College"
    },
    {
      "type": "association",
      "name": "Association"
    },
    {
      "type": "city",
      "name": "City / Municipality"
    },
    {
      "type": "union",
      "name": "Union"
    }
  ]
}
```

## 🏦 Plans

### 🛒 Get Plans

* **Path** `/plans`
* **Method** `GET`
* **Response**
```json
{
  "plans": [
    {
      "id":1,
      "name":"Basic",
      "stripeID":"stripe_123",
      "startingPrice": "9900",
      "organization":{
        "memberships":1,
        "subOrgs":1
      },
      "votingTypes":{
        "approval":true,
        "ranked":true,
        "weighted":true
      },
      "features":{
        "personalization":false,
        "emailReminder":true,
        "smsNotification":false
      }
    },
     "censusSizeTiers": [
    {
     "flatAmount":9900,
     "upTo":100
    },
    {
     "flatAmount":79900,
     "upTo":1500
    }
  ],
    ...
  ]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `500` | `50002` | `internal server error` |

### 🛍️ Get Plan info

* **Path** `/plans/{planID}`
* **Method** `GET`
* **Response**
```json
{
  "id":1,
  "name":"Basic",
  "stripeID":"stripe_123",
  "startingPrice": "9900",
  "organization":{
    "memberships":1,
    "subOrgs":1
  },
  "votingTypes":{
    "approval":true,
    "ranked":true,
    "weighted":true
  },
  "features":{
    "personalization":false,
    "emailReminder":true,
    "smsNotification":false
  },
  "censusSizeTiers": [
    {
     "flatAmount":9900,
     "upTo":100
    },
    {
     "flatAmount":79900,
     "upTo":1500
    }
  ],
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40004` | `malformed JSON body` |
| `404` | `40009` | `plan not found` |
| `500` | `50001` | `internal server error` |


## 🔰 Subscriptions

### 🛒 Create Checkout session

* **Path** `/subscriptions/checkout/`
* **Method** `POST`
* **Request Body** 
```json
{
  "lookupKey": 1, // PLan's corresponging DB ID
  "returnURL": "https://example.com/return",
  "address": "user@mail.com",
  "amount": 1000, // The desired maxCensusSize
}
```

* **Response**
```json
{
  "id": "cs_test_a1b2c3d4e5f6g7h8i9j0",
   // ... rest of stripe session attributes
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40023` | `plan not found` |
| `500` | `50002` | `internal server error` |

### 🛍️ Get Checkout session info

* **Path** `/subscriptions/checkout/{sessionID}`
* **Method** `GET`
* **Response**
```json
{
  "status": "complete", // session status
  "customer_email": "customer@example.com",
  "subscription_status": "active"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40023` | `session not found` |
| `500` | `50002` | `internal server error` |

### 🔗 Create Subscription Portal Session

* **Path** `/subscriptions/{orgAddress}/portal`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "portalURL": "https://portal.stripe.com/session/..."
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

## 📦 Storage

### 🌄 Upload image

* **Path** `/storage`
* **Method** `POST`

Accepting files uploaded by forms as such:
```html
<form action="http://localhost:8000" method="post" enctype="multipart/form-data">
  <p><input type="text" name="text" value="text default">
  <p><input type="file" name="file1">
  <p><input type="file" name="file2">
  <p><button type="submit">Submit</button>
</form>
```

* **Response**

```json
{
  "urls": ["https://file1.store.com","https://file1.store.com"]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40024` | `the obejct/parameters provided are invalid` |
| `500` | `50002` | `internal server error` |
| `500` | `50006` | `internal storage error` |


### 📄 Get object
This method return if exists, in inline mode. the image/file of the provided by the obectID

* **Path** `/storage/{objectID}`
* **Method** `GET`

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40024` | `the obejct/parameters provided are invalid` |
| `500` | `50002` | `internal server error` |
| `500` | `50006` | `internal storage error` |