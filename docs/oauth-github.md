# GitHub OAuth configuration

The first OAuth provider is GitHub. The authentication flow is provider-neutral so another provider, such as Google, can be added by implementing `internal/oauth.Provider` and registering it in the router.

## 1. Configure the encryption root key

Set `OAUTH_CONFIG_MASTER_KEY` to a Base64-encoded 32-byte random value. This key encrypts OAuth client secrets with AES-256-GCM and must be supplied consistently to every application instance.

PowerShell:

```powershell
$bytes = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
[Convert]::ToBase64String($bytes)
```

OpenSSL:

```bash
openssl rand -base64 32
```

Do not commit this value. Losing or changing it makes existing encrypted client secrets unreadable.

## 2. Create the GitHub OAuth App

Configure the GitHub authorization callback URL to exactly match:

```text
https://api.example.com/api/v1/public/oauth/github/callback
```

HTTPS is required outside local development. `http://localhost`, `http://127.0.0.1`, and `http://[::1]` are accepted for development.

The backend requests `read:user` and `user:email`. Automatic user registration and email matching require a verified GitHub email.

## 3. Save the provider configuration

As a super administrator, send:

```http
PUT /api/v1/admin/auth/oauth/providers/github
Authorization: Bearer <admin-access-token>
Content-Type: application/json
```

```json
{
  "display_name": "GitHub",
  "client_id": "github-client-id",
  "client_secret": "github-client-secret",
  "callback_url": "https://api.example.com/api/v1/public/oauth/github/callback",
  "user_redirect_url": "https://app.example.com/oauth/callback",
  "admin_redirect_url": "https://admin.example.com/oauth/callback",
  "user_enabled": true,
  "admin_enabled": true
}
```

Configuration responses return `"client_secret": "********"` and never return plaintext. Sending the mask back preserves the existing secret; send `"clear_secret": true` to remove it.

## Login behavior

- Users may log in or auto-register through GitHub. Existing users are matched only by a verified email.
- Administrators are never auto-created or matched by email. They must first sign in with their password and bind GitHub through `/api/v1/admin/auth/oauth/github/authorize`.
- OAuth callbacks redirect with a 60-second one-time `code`. Exchange it at `POST /api/v1/public/oauth/exchange`; JWTs are never placed in callback URLs.
- GitHub access tokens are used only to fetch the identity and verified email, then discarded.
