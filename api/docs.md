# API Docs

<details>
  <summary>Table of contents</summary>
  <br/>

- [🔐 Auth](#-auth)
  - [🔑 Login](#-login)
  - [🌐 OAuth Login](#-oauth-login)
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
  - [🧑‍🤝‍🧑 Organization users](#-organization-users)
  - [🧑‍💼 Invite organization user](#-invite-organization-user)
  - [⏳ List pending invitations](#-list-pending-invitations)
  - [🗑️ Delete pending invitation](#-delete-pending-invitation)
  - [🔄 Update pending invitation](#-update-pending-invitation)
  - [🤝 Accept organization invitation](#-accept-organization-invitation)
  - [🔄 Update organization user role](#-update-organization-user-role)
  - [❌ Remove organization user](#-remove-organization-user)
  - [💸 Organization Subscription Info](#-organization-subscription-info)
  - [📊 Organization Censuses](#-organization-censuses)
  - [👥 Organization Members](#-organization-members)
  - [➕ Add Organization Members](#-add-organization-members)
  - [🔍 Check Add Members Job Status](#-check-add-members-job-status)
  - [❌ Delete Organization Members](#-delete-organization-members)
  - [📋 Organization Meta Information](#-organization-meta-information)
  - [🎫 Create Organization Ticket](#-create-organization-ticket)
  - [🤠 Available organization user roles](#-available-organization-user-roles)
  - [👥 Organization Member Groups](#-organization-member-groups)
  - [🔍 Get Organization Member Group](#-get-organization-member-group)
  - [🆕 Create Organization Member Group](#-create-organization-member-group)
  - [🔄 Update Organization Member Group](#-update-organization-member-group)
  - [❌ Delete Organization Member Group](#-delete-organization-member-group)
  - [📋 List Organization Member Group Members](#-list-organization-member-group-members)
  - [✅ Validate Organization Member Group Data](#-validate-organization-member-group-data)
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
- [📊 Census](#-census)
  - [📝 Create Census](#-create-census)
  - [ℹ️ Get Census Info](#ℹ%EF%B8%8F-get-census-info)
  - [👥 Add Members](#-add-members)
  - [📢 Publish Census](#-publish-census)
  - [📋 Get Published Census Info](#-get-published-census-info)
  - [📢 Publish Group Census](#-publish-group-census)
  - [👥 Get Census Participants](#-get-census-participants)
- [🔄 Process](#-process)
  - [🆕 Create Process](#-create-process)
  - [ℹ️ Get Process Info](#-get-process-info)
  - [🗑️ Delete Process](#-delete-process)
  - [🔐 Process Authentication](#-process-authentication)
  - [🔒 Two-Factor Authentication](#-two-factor-authentication)
  - [✍️ Two-Factor Signing](#-two-factor-signing)
- [📦 Process Bundles](#-process-bundles)
  - [🆕 Create Process Bundle](#-create-process-bundle)
  - [➕ Add Processes to Bundle](#-add-processes-to-bundle)
  - [ℹ️ Get Process Bundle Info](#ℹ️-get-process-bundle-info)
  - [🔐 Process Bundle Authentication](#-process-bundle-authentication)
  - [✍️ Process Bundle Signing](#-process-bundle-signing)

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

### 🌐 OAuth Login

* **Path** `/oauth/login`
* **Method** `POST`
* **Request Body** 
```json
{
    "email": "my@email.me",
    "firstName": "Steve",
    "lastName": "Urkel",
    "oauthSignature": "<signature_from_oauth_service>",
    "userOAuthSignature": "<user_signature_on_oauth_signature>",
    "address": "0x..."
}
```

* **Description**
Authenticates a user using OAuth. If the user doesn't exist, a new account is created with the provided information. The endpoint performs two signature verifications:
1. Verifies the user's signature (`userOAuthSignature`) against the OAuth signature
2. Verifies the OAuth service's signature (`oauthSignature`) against the user's email

* **Response**
```json
{
  "token": "<jwt_token>",
  "expirity": "2024-08-21T11:26:54.368718+02:00",
  "registered": "true"  // returns true when a new user is added in the DB
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |
| `500` | `50007` | `OAuth server connection failed` |

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
        "website": "",
        "createdAt": "2025-01-16T11:56:04Z",
        "type": "company",
        "description": "My amazing testing organization",
        "size": 10,
        "color": "#ff0000",
        "logo": "https://[...].png",
        "subdomain": "mysubdomain",
        "timezone": "GMT+2",
        "active": true,
        "communications": false,
        "parent": {
          "...": "..."
        },
        "subscription": {
          "planID": 2,
          "startDate": "2025-01-16T11:56:04.079Z",
          "renewalDate": "0001-01-01T00:00:00Z",
          "lastPaymentDate": "0001-01-01T00:00:00Z",
          "active": true,
          "maxCensusSize": 50,
          "email": ""
        },
        "counters": {
          "sentSMS": 0,
          "sentEmails": 0,
          "subOrgs": 0,
          "users": 0
        }
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
  "type": "company",
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

* **Response**
```json
{
  "address": "0x23eE5d3ECE54a275FD75cF25E77C3bBeCe3CF3f7",
  "website": "",
  "createdAt": "2025-01-16T11:56:04Z",
  "type": "company",
  "size": "10",
  "color": "#ff0000",
  "subdomain": "mysubdomain",
  "country": "Spain",
  "timezone": "GMT+2",
  "active": true,
  "communications": false,
  "parent": {
    "...": {}
  },
  "subscription": {
    "planID": 2,
    "startDate": "2025-01-16T11:56:04.079Z",
    "renewalDate": "0001-01-01T00:00:00Z",
    "lastPaymentDate": "0001-01-01T00:00:00Z",
    "active": true,
    "maxCensusSize": 50,
    "email": ""
  },
  "counters": {
    "sentSMS": 0,
    "sentEmails": 0,
    "subOrgs": 0,
    "users": 0
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
  "type": "company",
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
  "address": "0x23eE5d3ECE54a275FD75cF25E77C3bBeCe3CF3f7",
  "website": "",
  "createdAt": "2025-01-16T11:56:04Z",
  "type": "company",
  "size": "10",
  "color": "#ff0000",
  "subdomain": "mysubdomain",
  "country": "Spain",
  "timezone": "GMT+2",
  "active": true,
  "communications": false,
  "parent": {
    "...": {}
  },
  "subscription": {
    "planID": 2,
    "startDate": "2025-01-16T11:56:04.079Z",
    "renewalDate": "0001-01-01T00:00:00Z",
    "lastPaymentDate": "0001-01-01T00:00:00Z",
    "active": true,
    "maxCensusSize": 50,
    "email": ""
  },
  "counters": {
    "sentSMS": 0,
    "sentEmails": 0,
    "subOrgs": 0,
    "users": 0
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

### 🧑‍🤝‍🧑 Organization users

* **Path** `/organizations/{address}/users`
* **Method** `GET`
* **Response**
```json
{
  "users": [
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

### 🧑‍💼 Invite organization user

* **Path** `/organizations/{address}/users`
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

* **Path** `/organizations/{address}/users/pending`
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

### 🗑️ Delete pending invitation

* **Path** `/organizations/{address}/users/pending/{invitationID}`
* **Method** `DELETE`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Description**
Delete a pending invitation for a user to join an organization by email. Only admins of the organization can delete invitations. The invitation must exist and belong to the specified organization.

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invitation code not provided` |
| `400` | `40005` | `invalid data - invitation not found` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 🔄 Update pending invitation

* **Path** `/organizations/{address}/users/pending/{invitationID}`
* **Method** `PUT`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Description**
Update the code, link and expiration time of a pending invitation to an organization by email. Resend the invitation email. Only admins of the organization can update an invitation. The invitation must exist and belong to the specified organization.

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invitation ID not provided` |
| `400` | `40005` | `invalid data - invitation not found` |
| `400` | `40011` | `no organization provided` |
| `409` | `40901` | `duplicate conflict - user is already invited to the organization` |
| `500` | `50002` | `internal server error` |

### 🤝 Accept organization invitation

* **Path** `/organizations/{address}/users/accept`
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

### 🔄 Update organization user role

* **Path** `/organizations/{address}/users/{userid}`
* **Method** `PUT`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "role": "manager"
}
```

* **Description**
Update the role of a user in an organization. Only admins of the organization can update the role.

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invalid user data` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### ❌ Remove organization user

* **Path** `/organizations/{address}/users/{userid}`
* **Method** `DELETE`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Description**
Remove a user from the organization. Only admins of an organization can remove a user. An admin cannot remove themselves from the organization.
**If a user does not exist, or has no role in the organization, no error is returned**

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40005` | `invalid user data - user cannot remove itself from the organization` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
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
    "users":0
  },
  "plan":{
    "id":3,
    "name":"free",
    "stripeID":"stripe_789",
    "default":true,
    "organization":{
      "users":10,
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

### 📊 Organization censuses
* **Path** `/organizations/{address}/censuses`
* **Method** `GET`
* **Response**
```json
{
  "censuses": [
    {
      "censusID": "<censusID>",
      "type": "<censusType>",
      "orgAddress": "<orgAddress>"
    },
    {
      "censusID": "<censusID>",
      "type": "<censusType>",
      "orgAddress": "<orgAddress>"
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
| `500` | `50002` | `internal server error` |

### 👥 Organization Members

* **Path** `/organizations/{address}/members`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Query params**
  * `page` - Page number (default: 1)
  * `limit` - Number of items per page (default: 10)
  * `search` - Search term
* **Response**
```json
{
  "pages": 10, // Total number of pages
  "page": 1, // Current page
  "members": [  // Currently sorted alphabetically by name
    {
      "id": "internal-uid1",
      "memberNumber": "12345",
      "name": "John",
      "surname": "Doe",
      "nationalID": "12345678A",
      "birthDate": "1990-05-15",
      "email": "john@example.com",
      "phone": "7890",
      "other": {
        "department": "Engineering",
        "position": "Developer"
      }
    },
    {
      "id": "internal-uid2",
      "memberNumber": "67890",
      "name": "Jane",
      "surname": "Smith",
      "nationalID": "87654321B",
      "birthDate": "1985-12-03",
      "email": "jane@example.com",
      "phone": "54321",
      "other": {
        "department": "Marketing",
        "position": "Manager"
      }
    }
  ]
}
```

* **Description**
Retrieves all members of an organization with pagination support. Requires Manager or Admin role for the organization.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### ➕ Add Organization Members

* **Path** `/organizations/{address}/members`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Query params**
  * `async` - Process asynchronously and return job ID (default: false)
* **Request body**
```json
{
  "members": [
    {
      "memberNumber": "12345",
      "name": "John",
      "surname": "Doe",
      "nationalID": "12345678A",
      "birthDate": "1990-05-15",
      "email": "john@example.com",
      "phone": "+1234567890",
      "password": "secretpass",
      "other": {
        "department": "Engineering",
        "position": "Developer"
      }
    },
    {
      "memberNumber": "67890",
      "name": "Jane",
      "surname": "Smith",
      "nationalID": "87654321B",
      "birthDate": "1985-12-03",
      "email": "jane@example.com",
      "phone": "+0987654321",
      "password": "secretpass",
      "other": {
        "department": "Marketing",
        "position": "Manager"
      }
    },
    {
      "memberNumber": "11111",
      "name": "Carlos",
      "nationalID": "99887766E",
      "birthDate": "1988-07-22",
      "email": "carlos@example.com",
      "phone": "+1555123456",
      "password": "secretpass",
      "other": {
        "department": "Finance"
      }
    }
  ]
}
```

**Note**: The new fields `surname`, `nationalID`, and `birthDate` are optional. If not provided, they will be stored as empty strings. The `birthDate` field should be in YYYY-MM-DD format.

* **Response (Synchronous)**
```json
{
  "count": 2
}
```

* **Response (Asynchronous)**
```json
{
  "jobID": "deadbeef"
}
```

* **Description**
Adds multiple members to an organization. Requires Manager or Admin role for the organization. Can be processed synchronously or asynchronously. If processed asynchronously, returns a job ID that can be used to check the status of the operation.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 🔍 Check Add Members Job Status

* **Path** `/organizations/{address}/members/job/{jobid}`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Response**
```json
{
  "progress": 75,
  "added": 150,
  "total": 200
}
```

* **Description**
Checks the progress of a job to add members to an organization. Returns the progress percentage, number of members added so far, and total number of members to add. If the job is completed (progress = 100), the job information is automatically deleted after 60 seconds.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40010` | `malformed URL parameter` |
| `404` | `40404` | `job not found` |
| `500` | `50002` | `internal server error` |

### ❌ Delete Organization Members

* **Path** `/organizations/{address}/members`
* **Method** `DELETE`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "IDs": ["internal-uid1","internal-uid2"]
}
```

* **Response**
```json
{
  "count": 2
}
```

* **Description**
Deletes multiple members from an organization by their member IDs. Requires Manager or Admin role for the organization. Returns the number of members successfully deleted.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 🎫 Create Organization Ticket

* **Path** `/organizations/{address}/ticket`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "type": "support",
  "title": "Need help with voting process",
  "description": "I'm having trouble setting up a new voting process. Can you help?"
}
```

* **Description**
Creates a new support ticket for the organization. The user must have any role in the organization. The ticket is sent to the support team via email.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40011` | `no organization provided` |
| `500` | `50002` | `internal server error` |

### 👥 Organization Member Groups

* **Path** `/organizations/{address}/groups`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Query params**
  * `page` - Page number (default: 1)
  * `limit` - Number of items per page (default: 10)
* **Description**
Get the list of groups and their info of the organization. Does not return the members of the groups, only the groups themselves. Requires admin or manager role.

* **Response**
```json
{
  "groups": [
    {
      "id": "group_id_hex",
      "title": "Development Team",
      "description": "Software development group",
      "createdAt": "2025-01-16T11:56:04Z",
      "updatedAt": "2025-01-16T11:56:04Z",
      "censusIDs": ["census_id_1", "census_id_2"],
      "membersCount": 5
    }
  ]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### 🔍 Get Organization Member Group

* **Path** `/organizations/{address}/groups/{groupID}`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
Get the information of an organization member group by its ID. Requires admin or manager role.

* **Response**
```json
{
  "id": "group_id_hex",
  "title": "Development Team",
  "description": "Software development group",
  "memberIDs": ["member_id_1", "member_id_2"],
  "censusIDs": ["census_id_1", "census_id_2"],
  "createdAt": "2025-01-16T11:56:04Z",
  "updatedAt": "2025-01-16T11:56:04Z"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40005` | `group ID is required` |
| `400` | `40005` | `group not found` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### 🆕 Create Organization Member Group

* **Path** `/organizations/{address}/groups`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
Create an organization member group with the given members. Requires admin or manager role.

* **Request body**
```json
{
  "title": "Development Team",
  "description": "Software development group",
  "memberIDs": ["member_id_1", "member_id_2"]
}
```

* **Response**
```json
{
  "id": "group_id_hex"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `organization not found` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### 🔄 Update Organization Member Group

* **Path** `/organizations/{address}/groups/{groupID}`
* **Method** `PUT`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
Update an organization member group changing the info, and adding or removing members. Requires admin or manager role.

* **Request body**
```json
{
  "title": "Updated Development Team",
  "description": "Updated software development group",
  "addMembers": ["new_member_id_1", "new_member_id_2"],
  "removeMembers": ["old_member_id_1"]
}
```

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `group ID is required` |
| `400` | `40005` | `group not found` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### ❌ Delete Organization Member Group

* **Path** `/organizations/{address}/groups/{groupID}`
* **Method** `DELETE`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
Delete an organization member group by its ID. Requires admin or manager role.

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40005` | `group ID is required` |
| `400` | `40005` | `group not found` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### 📋 List Organization Member Group Members

* **Path** `/organizations/{address}/groups/{groupID}/members`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Query params**
  * `page` - Page number (default: 1)
  * `limit` - Number of items per page (default: 10)
* **Description**
Get the list of members with details of an organization member group. Requires admin or manager role.

* **Response**
```json
{
  "totalPages": 5,
  "currentPage": 1,
  "members": [
    {
      "participantNo": "12345",
      "name": "John Doe",
      "email": "john@example.com",
      "phone": "+1234567890"
    },
    {
      "participantNo": "67890",
      "name": "Jane Smith",
      "email": "jane@example.com",
      "phone": "+0987654321"
    }
  ]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40005` | `group ID is required` |
| `400` | `40005` | `group not found` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### ✅ Validate Organization Member Group Data

* **Path** `/organizations/{address}/groups/{groupID}/validate`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
Validates that either AuthFields or TwoFaFields are provided or members in the specified group. Checks the AuthFields for duplicates or empty fields and the TwoFaFields for empty ones. Requires admin or manager role.

**Possible values for authFields:**
- "name" - Member's name
- "surname" - Member's surname
- "memberNumber" - Member's unique number
- "nationalID" - Member's national ID
- "birthDate" - Member's birth date

**Possible values for twoFaFields:**
- "email" - Member's email address
- "phone" - Member's phone number

* **Request body**
```json
{
  "authFields": [
    "name",
    "memberNumber",
    "nationalID"
  ],
  "twoFaFields": [
    "email",
    "phone"
  ]
}
```

* **Response**
```json
"OK"
```

* **Error Response**
In case of empty or duplicate fields, the error code `40005` is returned with the IDs of the corresponding members
```json
{
  "error": {
    "code": 40005,
    "message": "Invalid input data",
    "data": {
      "members": ["id5","id6","id7"], // member ids with valid data
      "missingData": ["id1","id2"],
      "duplicates": ["id3","id4"]
    }
  }
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `group ID is required` |
| `400` | `40005` | `missing both AuthFields and TwoFaFields` |
| `400` | `40005` | `invalid input data` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### 📋 Organization Meta Information

* **Path** `/organizations/{address}/meta`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
  Adds or overwrites meta information for an organization. Requires Manager or Admin role for the organization.
* **Request body**
```json
{
  "meta": {
    "key1": "value1",
    "key2": "value2",
    "nestedKey": {
      "subKey1": "subValue1"
    }
  }
}
```

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `403` | `40001` | `user is not a manager or admin of organization` |
| `400` | `40011` | `no organization provided` |
| `422` | `40005` | `invalid meta information` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

* **Path** `/organizations/{address}/meta`
* **Method** `PUT`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
  Updates existing or adds new key/value pairs in the meta information of an organization. Requires Manager or Admin role for the organization.
  Has only one layer o depth, if a second layer document is provided, for example meta.doc = [a,b,c]  all the document will be updated
* **Request body**
```json
{
  "meta": {
    "key1": "updatedValue1",
    "newKey": "newValue"
  }
}
```

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `403` | `40001` | `user is not a manager or admin of organization` |
| `400` | `40011` | `no organization provided` |
| `422` | `40005` | `invalid meta information` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

* **Path** `/organizations/{address}/meta`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
  Gets the meta information of an organization. Requires Manager or Admin role for the organization.
* **Response**
```json
{
  "meta": {
    "key1": "value1",
    "key2": "value2",
    "nestedKey": {
      "subKey1": "subValue1"
    }
  }
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `403` | `40001` | `user is not a manager or admin of organization` |
| `400` | `40011` | `no organization provided` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

* **Path** `/organizations/{address}/meta`
* **Method** `DELETE`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
  Deletes specific keys from the meta information of an organization. Requires Manager or Admin role for the organization.
* **Request body**
```json
{
  "keys": ["key1", "nestedKey.subKey1"]
}
```

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `403` | `40001` | `user is not a manager or admin of organization` |
| `400` | `40011` | `no organization provided` |
| `422` | `40005` | `invalid meta information` |
| `404` | `40009` | `organization not found` |
| `500` | `50002` | `internal server error` |

### 🤠 Available organization user roles
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
      "type": "others",
      "name": "Others"
    },
    {
      "type": "company",
      "name": "Company"
    },
    {
      "type": "political_party",
      "name": "Political Party"
    },
    {
      "type": "nonprofit",
      "name": "Nonprofit / NGO"
    },
    {
      "type": "association",
      "name": "Association"
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
        "users":1,
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
    "users":1,
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

## 📊 Census

### 📝 Create Census

* **Path** `/census`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
  Creates a new census for an organization. Requires Manager/Admin role.
  Validates that either AuthFields or TwoFaFields are provided.
  
  **Possible values for authFields:**
  - "name" - Member's name
  - "surname" - Member's surname
  - "memberNumber" - Member's unique number
  - "nationalID" - Member's national ID
  - "birthDate" - Member's birth date
  
  **Possible values for twoFaFields:**
  - "email" - Member's email address
  - "phone" - Member's phone number
  
* **Request body**
```json
{
  "type": "sms_or_mail",
  "orgAddress": "0x...",
  "authFields": [             // At least one of authFields or twoFaFields must be provided
    "name",
    "memberNumber",
    "nationalID"
  ],
  "twoFaFields": [            // Optional: defines which member data should be used for two-factor authentication
    "email",
    "phone"
  ]
}
```

* **Response**
Returns the census ID
```json
{
  "ID": "67bdfcfaeeb24a44660ec461"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `missing both AuthFields and TwoFaFields` |
| `400` | `40030` | `invalid census data` |
| `500` | `50002` | `internal server error` |

### ℹ️ Get Census Info

* **Path** `/census/{id}`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "id": "census_id",
  "type": "sms_or_mail",
  "orgAddress": "0x...",
  "createdAt": "2025-02-18T17:12:00Z"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40010` | `malformed URL parameter` |
| `500` | `50002` | `internal server error` |

### 👥 Add Members

* **Path** `/census/{id}`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "memberIds": ["66f2d6f0c7a6d022b96c5d30", "66f2d6f0c7a6d022b96c5d31"]
}
```

* **Response**
```json
{
  "added": 2
}
```

* **Description**
Adds existing organization members to an existing census by their member IDs. Requires Manager or Admin role for the organization that owns the census. If `memberIds` is empty, the endpoint returns `{"added": 0}`.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40010` | `census not found` |
| `400` | `40037` | `invalid data provided` |
| `500` | `50002` | `internal server error` |

### 📢 Publish Census

* **Path** `/census/{id}/publish`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Description**
Publishes a census, making it available for voting. Requires Manager or Admin role for the organization that owns the census. Currently only supports census type "sms_or_mail". The published census includes credentials necessary for the voting process.

* **Response**
```json
{
  "census": {
    "id": "census_id",
    "type": "sms_or_mail",
    "orgAddress": "0x...",
    "createdAt": "2025-02-18T17:12:00Z"
  },
  "uri": "https://example.com/csp/",
  "root": "public_key"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin of organization` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40010` | `missing census ID` |
| `500` | `50002` | `internal server error` |

### 📋 Get Published Census Info

* **Path** `/census/{id}/publish`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "uri": "https://example.com/process/",
  "root": "public_key"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40010` | `malformed URL parameter` |
| `500` | `50002` | `internal server error` |

### 📢 Publish Group Census

* **Path** `/census/{id}/publish/group/{groupid}`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
  Publishes a census based on a specific organization members group for voting. Requires Manager/Admin role.
  Returns published census with credentials.

* **Request body**
```json
{
  "authFields": [             // At least one of authFields or twoFaFields must be provided
    "name",
    "memberNumber",
    "nationalID"
  ],
  "twoFaFields": [            // Optional: defines which member data should be used for two-factor authentication
    "email",
    "phone"
  ]
}
```

* **Response**
```json
{
  "uri": "https://example.com/process/",
  "root": "public_key",
  "size": 10
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40010` | `invalid census ID or group ID` |
| `404` | `40404` | `census not found` |
| `500` | `50002` | `internal server error` |

### 👥 Get Census Participants

* **Path** `/census/{id}/participants`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Description**
  Retrieve participants of a census by ID. Requires Manager/Admin role.

* **Response**
```json
{
  "censusID": "census_id_string",
  "memberIDs": ["member_id_1", "member_id_2", "member_id_3"]
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40010` | `malformed URL parameter` |
| `404` | `40404` | `census not found` |
| `500` | `50002` | `internal server error` |

## 🔄 Process

### 🆕 Create Process

* **Path** `/process/{processId}`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "censusRoot": "published_census_root",
  "censusUri": "published_census_uri",
  "censusId": "used-census-id",
  "metadata": "base64_encoded_metadata" // optional

}
```

* **Response**
Returns 201 Created on success

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40010` | `malformed URL parameter` |
| `500` | `50002` | `internal server error` |

### 📈 Get Process Info

* **Path** `/process/{processId}`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "id": "process_id",
  "publishedCensus": {
    "census": {
      "id": "census_id",
      "type": "sms_or_mail",
      "orgAddress": "0x...",
      "createdAt": "2025-02-18T17:12:00Z"
    },
    "uri": "https://example.com/csp/",
    "root": "public_key"
  },
  "metadata": "base64_encoded_metadata",
  "orgID": "0x..."
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40010` | `malformed URL parameter` |
| `500` | `50002` | `internal server error` |

### 🗑️ Delete Process

* **Path** `/process/{processId}`
* **Method** `DELETE`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Description**
Deletes a voting process. Requires Manager or Admin role for the organization that owns the process. The process must exist and the user must have appropriate permissions.

* **Response**
```json
"OK"
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `user is not admin or manager of the organization that owns this process` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40010` | `missing process ID` |
| `400` | `40010` | `invalid process ID` |
| `400` | `40010` | `process not found` |
| `500` | `50002` | `internal server error` |

### 🔐 Process Authentication

* **Path** `/process/{processId}/auth`
* **Method** `POST`
* **Request Body**
```json
{
  "memberNumber": "012345",
  "email": "member@example.com",  // Optional: Required if using email authentication
  "phone": "+1234567890",             // Optional: Required if using phone authentication
  "password": "secretpass1234"        // Optional: Required if using password authentication
}
```

* **Response**
```json
{
  "ok": true
}
```

* **Description**
Validates a member's authentication for a process. The member must exist in both the organization and the published census. Authentication can be done via email, phone number, or password. At least one authentication method must be provided.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40004` | `malformed JSON body` |
| `400` | `40010` | `malformed URL parameter` |
| `401` | `40001` | `member not found` |
| `401` | `40001` | `member not found in census` |
| `401` | `40001` | `invalid user data` |
| `500` | `50002` | `internal server error` |


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

### 🔒 Two-Factor Authentication

* **Path** `/process/{processId}/auth/{step}`
* **Method** `POST`
* **Request Body (Step 0)** 
```json
{
  "memberNumber": "012345",
  "email": "member@example.com",  // Optional: Required if using email authentication
  "phone": "+1234567890",             // Optional: Required if using phone authentication
  "password": "secretpass1234"        // Optional: Required if using password authentication
}
```

* **Response (Step 0)**
```json
{
  "authToken": "uuid-string"
}
```

* **Request Body (Step 1)** 
```json
{
  "authToken": "uuid-string",
  "authData": ["verification-code-or-other-auth-data"]
}
```
* **Response (Step 1)**
```json
{
  "tokenR": "base64-encoded-date"
}
```

* **Description**
Two-step authentication process for voters. Step 0 initiates the authentication process and returns an auth token. Step 1 completes the authentication by providing the verification code or other authentication data.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40004` | `malformed JSON body` |
| `400` | `40010` | `malformed URL parameter` |
| `401` | `40001` | `user not authorized` |
| `500` | `50002` | `internal server error` |

### ✍️ Two-Factor Signing

* **Path** `/process/{processId}/sign`
* **Method** `POST`
* **Request Body** 
```json
{
  "token": "base64-encoded-token",
  "payload": "base64-encoded-payload"
}
```

* **Response**
```json
{
  "signature": "base64-encoded-signature"
}
```

* **Description**
Signs a payload using two-factor authentication. Requires a valid tokenR obtained from the two-factor authentication process.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40004` | `malformed JSON body` |
| `400` | `40010` | `malformed URL parameter` |
| `401` | `40001` | `user not authorized` |
| `500` | `50002` | `internal server error` |

## 📦 Process Bundles

Process bundles allow grouping multiple processes together with a single census, enabling users to participate in multiple voting processes using the same authentication mechanism.

### 🆕 Create Process Bundle

* **Path** `/process/bundle`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "censusId": "census_id_string",
  "processIds": ["process_id_1", "process_id_2", "..."]
}
```

* **Response**
```json
{
  "uri": "https://example.com/process/bundle/bundle_id",
  "root": "census_root_public_key"
}
```

* **Description**
Creates a new process bundle with the specified census and optional list of processes. Requires Manager or Admin role for the organization that owns the census. The census root will be the same as the account's public key.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40006` | `missing process ID` |
| `400` | `40007` | `invalid process ID` |
| `500` | `50002` | `internal server error` |

### ➕ Add Processes to Bundle

* **Path** `/process/bundle/{bundleId}`
* **Method** `PUT`
* **Headers**
  * `Authentication: Bearer <user_token>`
* **Request body**
```json
{
  "processes": ["process_id_1", "process_id_2", "..."]
}
```

* **Response**
```json
{
  "uri": "/process/bundle/bundle_id",
  "root": "census_root_public_key"
}
```

* **Description**
Adds additional processes to an existing bundle. Requires Manager or Admin role for the organization that owns the bundle. The processes array must not be empty.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40006` | `missing process ID` |
| `400` | `40007` | `invalid process ID` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40011` | `no processes provided` |
| `500` | `50002` | `internal server error` |

### ℹ️ Get Process Bundle Info

* **Path** `/process/bundle/{bundleId}`
* **Method** `GET`
* **Response**
```json
{
  "id": "bundle_id",
  "census": {
    "id": "census_id",
    "type": "sms_or_mail",
    "orgAddress": "0x...",
    "createdAt": "2025-02-18T17:12:00Z"
  },
  "censusRoot": "census_root_public_key",
  "orgAddress": "0x...",
  "processes": ["process_id_1", "process_id_2", "..."]
}
```

* **Description**
Retrieves information about a process bundle by its ID, including the associated census and list of processes.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40011` | `missing bundle ID` |
| `500` | `50002` | `internal server error` |

### 🔐 Process Bundle Authentication

* **Path** `/process/bundle/{bundleId}/auth/{step}`
* **Method** `POST`
* **Request Body (Step 0)** 
```json
{
  "memberNumber": "012345",
  "name": "John",
  "surname": "Doe",
  "nationalID": "12345678A",
  "birthDate": "1990-05-15",
  "email": "member@example.com",  // Optional: Required if using email authentication
}
```

* **Response (Step 0)**
```json
{
  "authToken": "uuid-string"
}
```

* **Request Body (Step 1)** 
```json
{
  "authToken": "uuid-string",
  "authData": ["verification-code-or-other-auth-data"]
}
```

* **Response (Step 1)**
```json
{
  "authToken": "uuid-string"
}
```

* **Description**
Two-step authentication process for voters participating in a bundle of processes:

1. **Step 0**: The user provides identifying information (fields depend on what the census requires for authentication). If valid, the server:
   - Verifies the participant is in the census using the login hash generated from the provided fields
   - Determines the appropriate contact method based on census type (email, SMS, or none for auth-only censuses)
   - Sends a challenge to the user's contact method (except for auth-only censuses)
   - Returns an auth token for use in Step 1

2. **Step 1**: The user submits the auth token along with:
   - For censuses that require challenge verification: the verification code received via email/SMS
   - For auth-only censuses: no challenge solution required if the token is already verified

If valid, the token is marked as verified and returned. The verified token can then be used for signing processes in the bundle.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40004` | `malformed JSON body` |
| `400` | `40005` | `invalid user data` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40011` | `missing bundle ID` |
| `400` | `40012` | `wrong step ID` |
| `400` | `40013` | `bundle has no processes` |
| `401` | `40001` | `user not authorized` |
| `500` | `50002` | `internal server error` |

### ✍️ Process Bundle Signing

* **Path** `/process/bundle/{bundleId}/sign`
* **Method** `POST`
* **Request Body** 
```json
{
  "authToken": "uuid-string",
  "processID": "hex-string-process-id",
  "payload": "hex-string-address"
}
```

* **Response**
```json
{
  "signature": "base64-encoded-signature"
}
```

* **Description**
Signs a process in a bundle. This endpoint performs several verification steps:

1. Verifies the auth token is valid and has been verified through the authentication process
2. Confirms that the requested process is part of the bundle
3. Verifies that the participant associated with the token is in the census
4. Signs the provided address (payload) with the user's cryptographic key

Once signed, the process is marked as consumed and cannot be signed again with the same token.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40004` | `malformed JSON body` |
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40011` | `missing bundle ID` |
| `400` | `40013` | `bundle has no processes` |
| `401` | `40001` | `user not authorized` |
| `401` | `40001` | `process not found in bundle` |
| `401` | `40001` | `participant not found in the census` |
| `500` | `50002` | `internal server error` |

### 📋 Get Process Bundle Member Info

* **Path** `/process/bundle/{bundleId}/{participantID}`
* **Method** `GET`
* **Response**
```json
[
  {
    "electionId": "1234567890abcdef1234567890abcdef1234567890abcdef",
    "remainingAttempts": 3,
    "consumed": false,
    "extra": ["Additional information", "More details"],
    "voted": "abcdef1234567890abcdef1234567890abcdef1234"
  },
  {
    "electionId": "abcdef1234567890abcdef1234567890abcdef1234567890",
    "remainingAttempts": 0,
    "consumed": true,
    "extra": ["Process completed"],
    "voted": "1234567890abcdef1234567890abcdef1234567890"
  }
]
```

* **Description**
Retrieves process information for a specific member in a process bundle. Returns an array of election objects containing details such as the election ID, remaining voting attempts, consumption status, and additional metadata. If no elections are found for the member, returns an empty array.

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `400` | `40010` | `malformed URL parameter` |
| `400` | `40010` | `missing bundle ID` |
| `400` | `40010` | `invalid bundle ID` |
| `400` | `40010` | `missing member ID` |
| `500` | `50002` | `internal server error` |
