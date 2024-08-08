# API Docs

<details>
  <summary>Table of contents</summary>
  <br/>

- [Auth](#auth)
  - [Login](#login)
  - [Refresh token](#refresh-token)
- [Users](#users)
  - [Register](#register)
  - [Get current user info](#get-current-user-info)
  - [Update current user info](#update-current-user-info)
  - [Update current user password](#update-current-user-password)
- [Organizations](#organizations)
  - [Create organization](#create-organization)
  - [Update organization](#update-organization)
  - [Organization info](#organization-info)
- [Transactions](#transactions)
  - [Sign tx](#sign-tx)
  - [Sign message](#sign-message)

</details>

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
    "fullName": "Steve Urkel",
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

### Get current user info

* **Path** `/users/me`
* **Method** `GET`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Response**
```json
{
  "email": "test@test.test",
  "fullName": "steve_urkel",
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

### Update current user info

* **Path** `/users/me`
* **Method** `PUT`
* **Request body**
```json
{
    "email": "my@email.me",
    "fullName": "Steve Urkel",
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

### Update current user password

* **Path** `/users/me/password`
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

## Organizations

### Create organization

* **Path** `/organizations`
* **Method** `POST`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Request body**
```json
{
  "name": "Test Organization",
  "type": "community",
  "description": "My amazing testing organization",
  "size": 10,
  "color": "#ff0000",
  "logo": "https://[...].png",
  "subdomain": "mysubdomain",
  "timezone": "GMT+2",
  "active": true,
}
```
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
| `500` | `50002` | `internal server error` |

### Update organization

* **Path** `/organizations/{address}`
* **Method** `PUT`
* **Headers**
  * `Authentication: Bearer <user_token>`

* **Request body**
Only the following parameters can be changed. Every parameter is optional.
```json
{
  "name": "Test Organization",
  "description": "My amazing testing organization",
  "size": 10,
  "color": "#ff0000",
  "logo": "https://[...].png",
  "subdomain": "mysubdomain",
  "timezone": "GMT+2",
  "active": true,
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

### Organization info

* **Path** `/organizations/{address}`
* **Method** `GET`
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
  "timezone": "GMT+2",
  "active": true,
  "parent": {
    "...": "..."
  }
}
```

* **Errors**

| HTTP Status | Error code | Message |
|:---:|:---:|:---|
| `401` | `40001` | `user not authorized` |
| `400` | `40004` | `malformed JSON body` |
| `400` | `40009` | `organization not found` |
| `400` | `40010` | `malformed URL parameter` |
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