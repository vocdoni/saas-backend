# API Docs

## Auth

### Login

* **Path** `/auth`
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
| `500` | `50002` | `internal server error` |

### Refresh token

* **Path** `/auth/refresh`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

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
| `500` | `50002` | `internal server error` |

## Users

### Register

* **Path** `/users`
* **Method** `POST`
* **Request body**
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
| `400` | `40002` | `email malformed` |
| `400` | `40003` | `password too short` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |

### Current user info

* **Path** `/users/me`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "email": "test@test.test",
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
        "timezone": "GMT+2"
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

## Organizations

### Create organization

* **Path** `/organizations`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "name": "Test Organization",
  "type": "community",
  "description": "My amazing testing organization",
  "size": 10,
  "color": "#ff0000",
  "logo": "https://[...].png",
  "subdomain": "mysubdomain",
  "timezone": "GMT+2"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |

## Transactions

### Sign tx

* **Path** `/transactions`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "organizationAddress": "0x...",
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

### Sign message

* **Path** `/transactions/message`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "organizationAddress": "0x...",
  "payload": "<payload_to_sign>"
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `500` | `50002` | `internal server error` |