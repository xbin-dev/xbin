# 2026-08-02 — transfer-into-org now requires the Create knob (D39)

**What changed.** `POST /owner` (and every transfer UI) now gates the
RECEIVE side: moving a tile into `org:X` requires holding X's **Create**
knob (or org-adminship of X, or ws-admin). Previously plain membership
sufficed. Rationale: receiving a tile is creating one, capability-wise —
the old rule let a read-level member move arbitrary personal tiles into
org governance.

**Who to check.** Self-transfer flows where a low-level member moved their
own tiles into their org (the friend-group "transfer so my org admin can
approve" detour): those members now need `create` on the org
(`bx org member <org> <user> --create`). The 403 names the missing right.

Also in this change (additive): `GET /owner/preview` impact reports,
automatic unbinding of ceiling-dead binding slots on transfer (with
backend restarts so enforcement is immediate), and the executed report in
the transfer response.
