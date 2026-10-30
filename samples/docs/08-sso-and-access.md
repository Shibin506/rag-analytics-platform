# Single sign-on and team access

## Roles

- **Owner** — manages billing, API keys and members. Every account has at least one owner.
- **Admin** — manages schemas, documents and members, but not billing.
- **Analyst** — views dashboards and asks the assistant questions.
- **Viewer** — views dashboards only.

## SAML single sign-on

Enterprise accounts can require SAML 2.0 single sign-on with Okta, Azure AD (Microsoft Entra ID) or Google Workspace. Upload your identity provider's metadata XML under **Settings → Security**, then test the connection before enforcing it. Once SSO is enforced, password logins are disabled for everyone except owners, who keep a break-glass password.

## SCIM provisioning

With SCIM enabled, users added to or removed from the Acme group in your identity provider are created or deactivated automatically within 40 minutes.

## Audit log

Every change to members, roles, API keys and schemas is written to the audit log, which owners can export as CSV for the last 365 days.
