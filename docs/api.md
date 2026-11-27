API for Vocdoni SaaS Backend.

Errors are returned as JSON: `{"error":"message","code":4xxxx}`.
Codes 4xxxx are client errors (HTTP 4xx); codes 5xxxx are server errors (HTTP 5xx).

## Error Codes

| Code | HTTP Status | Message | Go name |
|------|-------------|---------|--------|
| 40001 | 401 Unauthorized | authentication required | `ErrUnauthorized` |
| 40002 | 400 Bad Request | invalid email format | `ErrEmailMalformed` |
| 40003 | 400 Bad Request | password must be at least 8 characters | `ErrPasswordTooShort` |
| 40004 | 400 Bad Request | invalid JSON request body | `ErrMalformedBody` |
| 40005 | 400 Bad Request | invalid user information provided | `ErrInvalidUserData` |
| 40006 | 400 Bad Request | transaction signing failed | `ErrCouldNotSignTransaction` |
| 40007 | 400 Bad Request | invalid transaction format | `ErrInvalidTxFormat` |
| 40008 | 400 Bad Request | transaction type not allowed | `ErrTxTypeNotAllowed` |
| 40009 | 404 Not Found | organization not found | `ErrOrganizationNotFound` |
| 40010 | 400 Bad Request | invalid URL parameter | `ErrMalformedURLParam` |
| 40011 | 400 Bad Request | organization address is required | `ErrNoOrganizationProvided` |
| 40012 | 404 Not Found | user has no organizations | `ErrNoOrganizations` |
| 40013 | 400 Bad Request | invalid organization information provided | `ErrInvalidOrganizationData` |
| 40014 | 401 Unauthorized | account email not verified | `ErrUserNoVerified` |
| 40015 | 400 Bad Request | user account is already verified | `ErrUserAlreadyVerified` |
| 40016 | 401 Unauthorized | verification code has expired | `ErrVerificationCodeExpired` |
| 40017 | 400 Bad Request | verification max attempts reached | `ErrVerificationMaxAttempts` |
| 40018 | 404 Not Found | user not found | `ErrUserNotFound` |
| 40019 | 400 Bad Request | invitation code has expired | `ErrInvitationExpired` |
| 40024 | 400 Bad Request | invalid storage object or parameters | `ErrStorageInvalidObject` |
| 40025 | 400 Bad Request | feature not supported | `ErrNotSupported` |
| 40026 | 404 Not Found | job not found | `ErrJobNotFound` |
| 40027 | 404 Not Found | census not found | `ErrCensusNotFound` |
| 40029 | 404 Not Found | census participant not found | `ErrCensusParticipantNotFound` |
| 40030 | 400 Bad Request | invalid census data provided | `ErrInvalidCensusData` |
| 40031 | 400 Bad Request | max drafts reached | `ErrMaxDraftsReached` |
| 40032 | 401 Unauthorized | user does not have admin role | `ErrUserHasNoAdminRole` |
| 40037 | 400 Bad Request | invalid data provided | `ErrInvalidData` |
| 40038 | 404 Not Found | process not found | `ErrProcessNotFound` |
| 40039 | 400 Bad Request | invalid or unsupported OAuth provider | `ErrInvalidOAuthProvider` |
| 40040 | 400 Bad Request | OAuth users cannot use password recovery | `ErrOAuthUserCannotUsePasswordRecovery` |
| 40041 | 400 Bad Request | cannot unlink last authentication method | `ErrCannotUnlinkLastAuthMethod` |
| 40042 | 400 Bad Request | OAuth provider already linked to this account | `ErrProviderAlreadyLinkedToThisAccount` |
| 40043 | 400 Bad Request | OAuth provider not linked to this account | `ErrProviderNotLinked` |
| 40044 | 400 Bad Request | OAuth provider already linked to another account | `ErrProviderAlreadyLinkedToAnotherAccount` |
| 40045 | 409 Conflict | user already has a password | `ErrUserAlreadyHasPassword` |
| 40048 | 400 Bad Request | user has reached maximum number of organizations | `ErrMaxOrganizationsReached` |
| 40101 | 401 Unauthorized | account is not registered using OAuth | `ErrNonOauthAccount` |
| 40102 | 401 Unauthorized | invalid login credentials | `ErrInvalidLoginCredentials` |
| 40103 | 401 Unauthorized | attempt cooldown time not reached | `ErrAttemptCoolDownTime` |
| 40145 | 401 Unauthorized | operation would exceed organization members limit | `ErrExceedsOrganizationMembersLimit` |
| 40150 | 403 Forbidden | the "All members" group is auto-generated and cannot be deleted | `ErrAutoGroupCannotBeDeleted` |
| 40151 | 403 Forbidden | membership of the auto-generated "All members" group cannot be manually modified | `ErrAutoGroupMembersCannotBeModified` |
| 40153 | 403 Forbidden | organization is not an integrator | `ErrNotAnIntegrator` |
| 40156 | 401 Unauthorized | invalid, revoked or expired API key | `ErrInvalidAPIKey` |
| 40157 | 403 Forbidden | API keys are not permitted for this endpoint | `ErrAPIKeyNotAllowed` |
| 40158 | 403 Forbidden | API key lacks the required scope | `ErrInsufficientAPIKeyScope` |
| 40159 | 400 Bad Request | invalid API key scope | `ErrInvalidAPIKeyScope` |
| 40160 | 404 Not Found | API key not found | `ErrAPIKeyNotFound` |
| 40161 | 409 Conflict | managed organization has active elections and cannot be deleted | `ErrManagedOrgHasActiveElections` |
| 40164 | 400 Bad Request | vote batch is empty | `ErrVoteBatchEmpty` |
| 40165 | 400 Bad Request | vote batch is too large | `ErrVoteBatchTooLarge` |
| 40166 | 400 Bad Request | vote batch spans several organizations | `ErrVoteBatchMixedOrganizations` |
| 40167 | 413 Request Entity Too Large | request body is too large | `ErrRequestBodyTooLarge` |
| 40171 | 409 Conflict | resource changed since it was read | `ErrStaleUpdate` |
| 40172 | 409 Conflict | stored questions do not match the process | `ErrProcessQuestionsMismatch` |
| 40173 | 409 Conflict | member has already been signed for in an ongoing process | `ErrCensusMemberAlreadySignedFor` |
| 40174 | 403 Forbidden | integrator top-level organizations cannot own elections; use a managed organization | `ErrIntegratorTopLevelOrgCannotOwnProcess` |
| 40175 | 402 Payment Required | insufficient integrator wallet balance | `ErrInsufficientWalletBalance` |
| 40176 | 422 Unprocessable Entity | census size requires a custom quote, self-service checkout is unavailable | `ErrQuoteRequired` |
| 40177 | 409 Conflict | a payment for this process is already processing or completed | `ErrPaymentSessionConflict` |
| 40178 | 402 Payment Required | process publication requires payment | `ErrPaymentRequired` |
| 40179 | 404 Not Found | checkout session not found | `ErrCheckoutSessionNotFound` |
| 40181 | 403 Forbidden | managed organizations have no wallet; their integrator pays | `ErrManagedOrgHasNoWallet` |
| 40182 | 400 Bad Request | max admin and manager users reached | `ErrMaxWritersReached` |
| 40183 | 402 Payment Required | managed organization spend cap reached | `ErrManagedOrgSpendCapReached` |
| 40184 | 402 Payment Required | top up the integrator wallet to create more managed organizations | `ErrManagedOrgTopUpRequired` |
| 40185 | 404 Not Found | wallet movement not found or has no invoice | `ErrWalletInvoiceNotFound` |
| 40199 | 403 Forbidden | requests must go through the public endpoint | `ErrOriginNotAllowed` |
| 40801 | 401 Unauthorized | voter weight cannot be zero | `ErrZeroWeightVoter` |
| 40802 | 401 Unauthorized | invalid authentication data | `ErrCSPAuthFailed` |
| 40901 | 409 Conflict | resource already exists | `ErrDuplicateConflict` |
| 40902 | 409 Conflict | update would create duplicates | `ErrUpdateWouldCreateDuplicates` |
| 40903 | 409 Conflict | process publish already in progress | `ErrPublishInProgress` |
| 40904 | 409 Conflict | process is partially published: some questions already have on-chain elections | `ErrProcessPartiallyPublished` |
| 50001 | 500 Internal Server Error | server error: failed to process response | `ErrMarshalingServerJSONFailed` |
| 50002 | 500 Internal Server Error | server error: operation failed | `ErrGenericInternalServerError` |
| 50003 | 500 Internal Server Error | server error: faucet package creation failed | `ErrCouldNotCreateFaucetPackage` |
| 50004 | 500 Internal Server Error | server error: blockchain request failed | `ErrVochainRequestFailed` |
| 50005 | 500 Internal Server Error | server error: payment processing failed | `ErrStripeError` |
| 50006 | 500 Internal Server Error | server error: storage operation failed | `ErrInternalStorageError` |
| 50007 | 500 Internal Server Error | server error: OAuth server connection failed | `ErrOAuthServerConnectionFailed` |
| 50008 | 500 Internal Server Error | server error: stripe webhook failed | `ErrStripeWebhookError` |
| 50301 | 503 Service Unavailable | transaction queue is full, retry later | `ErrTxQueueFull` |
| 50302 | 503 Service Unavailable | server is busy, retry later | `ErrServerBusy` |
| 50303 | 503 Service Unavailable | another transaction for this organization is in progress, retry later | `ErrOrgTxBusy` |
