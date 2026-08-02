# organisations

No backend, no roles — a pure frontend over the xbind ownership/org APIs
(`/api/xbin/whoami`, `/orgs`, `/access`, `/owner`, `/grants`, `/bindings`,
`/users-directory`; docs/protocol.md). Every request is made with the signed-in
user's own cookie session, so what each person can see and do here is exactly
what the server allows them (docs/auth.md, D24–D28).
