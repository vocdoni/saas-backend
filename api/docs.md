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
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `401` | `40014` | `user account not verified` |
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
  "language": "EN"
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
  "active": true
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