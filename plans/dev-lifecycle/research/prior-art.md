# Prior art: dev vs prod deployments on 27 platforms

> Status: live — research notes feeding the dev-lifecycle design ([../README.md](../README.md)). Read-only exploration, facts as of master 59687bf..926046d (2026-09-27); file:line references drift as the code moves — re-check before relying on a line number. Produced by: workflow dev-lifecycle-explore (wf_b2976d01-667), researcher `priorArt`.

## Cross-cutting synthesis

CROSS-CUTTING LESSONS
(Web facts come from the pages listed in each platform's sources. Anything marked 'inference' is my own reading. Repo references are context only.)

1. Every platform separates two objects
- An immutable artifact:
  - Version: Cloudflare, Lambda, App Engine, Apps Script, Firebase.
  - Revision: Cloud Run, Deno.
  - Deploy or deployment in the artifact sense: Vercel, Netlify, Deno Classic.
  - Slug and release: Heroku. Release: Retool, Fly.
- A named, long-lived pointer that owns traffic and config, and increasingly data:
  - Alias: Lambda.
  - Deployment: Cloudflare (which versions serve); Convex (a named runtime with its own data).
  - Timeline: Deno.
  - Environment: Vercel, Railway, Render, Retool; n8n instances.
  - Channel: Firebase. Slot: Azure. Stage: Heroku.
  - Branch: Val Town, Supabase, Neon, PlanetScale.
  - Head vs versioned deployment: Apps Script.
- Most pointers can "follow latest", and that mode can be switched off:
  - Apps Script head deployment: 'always syncs with the most recently saved code'.
  - Cloud Run LATEST: traffic and tags follow new revisions, but any manual pin makes all later deploys stop following until --to-latest.
  - Deno active revision: latest unless the timeline is locked.
  - Vercel: 'Auto-assign Custom Production Domains'. Netlify: auto publishing.
  - Retool: 'latest' is live until the first release.
  - n8n: 1.x made every save live; 2.0 requires Publish.
  - Convex and Amplify: the file watcher is bound to exactly one dev deployment or sandbox ('only one can be running at a time').
- Mapping to xbin (inference):
  - A tile corresponds to a Worker, function or app.
  - main and dev are pointers with their own data and config, like a Deno timeline, Convex deployment or Railway environment.
  - The tile's working tree is the mutable head, like $LATEST or Apps Script's head deployment.
  - Hot reload is a per-deployment "follows the working tree" flag that at most one deployment may hold.
  - Tiles that never opt in keep exactly one implicit deployment (main) that follows the working tree. That is Retool's model: live by default, releases opt-in, and 'Unpublish' returns to live mode. Nothing becomes legacy.

2. Pausing hot reload and "reload now"
- Direct precedents:
  - Netlify 'Stop auto publishing': builds continue and are 'ready for whenever you want to publish them'. 'Publish deploy' ships any ready build. After unlocking, 'previously built deploys will not auto-publish'.
  - Deno timeline locking: pushes still build but don't activate.
  - Vercel with auto-assign off: production builds are 'Staged' and promoted without a rebuild.
  - Cloud Run --no-traffic: the pin is sticky.
  - Dokku apps:lock: a hard freeze; pushes are rejected.
  - Piku PIKU_AUTO_RESTART=false: deploy without restarting, then restart by hand.
  - Fly `secrets set --stage` plus `secrets deploy`.
  - Railway staged changes: config changes wait for 'Deploy'.
  - PlanetScale gated deployments: the migration finishes, then waits for a manual 'Apply changes'.
- Semantics worth copying:
  - (a) Pausing stops activation, not building or checking. "Reload now" then activates a candidate that is already built and verified.
  - (b) A rollback must imply a pause, or the next save undoes it. Vercel turns auto-assign off after Instant Rollback and shows 'Undo Rollback'.
  - (c) Decide, and show, what resume does:
    - Netlify's unlock doesn't publish the backlog.
    - Cloud Run's --to-latest jumps straight to the newest revision.
  - (d) The paused state must be sticky and visible. Cloud Run's silent sticky pin is a documented surprise.
- Mapping (inference): pausing freezes a deployment's code pointer at a snapshot while the working tree keeps moving. D8's blue/green drain (plans/DECISIONS.md:306: '30 s then kill', instance credentials die at swap) already supplies the cutover mechanics for "reload now".

3. How code moves
- Deploy the current code to a chosen target: Convex `deploy`; Vercel `--prod` or `--target=staging`; `wrangler deploy --env` or `wrangler preview`; `firebase hosting:channel:deploy`.
- Promote A→B:
  - Heroku copies the slug with no rebuild; config and add-ons stay with the target; this requires stateless builds.
  - Firebase `hosting:clone` points a new release at the same version.
  - Cloudflare has 'Promote deployment'.
  - Vercel promote: a staged build needs no rebuild, but preview→production is a full rebuild with production env vars.
  - An Azure swap moves code plus non-sticky settings.
  - Lambda `update-alias`.
  - Apps Script: edit the deployment to point at a new version.
  - Retool, n8n: publish.
  - PlanetScale deploy request: diff, queue and a 30-minute revert window.
- The recurring failure is configuration baked into the artifact:
  - Lambda versions freeze environment variables, so promoting a version drags its config along (inference from the docs).
  - Heroku promotion is only safe for stateless builds.
  - Vercel must rebuild previews to swap their env.
  - Deno Classic only allowed promoting deployments that already used the production KV. A bug made `deployctl --prod` serve preview KV ('my prod domain showed all preview data').
  - Lesson (inference): promotion copies code only; the target deployment binds its own data and secrets when it starts, late-bound by deployment name.
- Git variants:
  - Branch-follows: Deno (one timeline per branch, active revision follows builds, lockable). Also Railway and Amplify (a trigger branch per environment), Vercel custom environments (branch tracking), Supabase (git branch ↔ Supabase branch), n8n (instance ↔ branch; 'don't push and pull to the same n8n instance').
  - Blessed push remote:
    - Heroku: per-app remote; 'Pushing code to another branch of the heroku remote has no effect'.
    - Dokku: a deploy-branch filter; the first pushed branch becomes it; other branches need a receive-branch plugin.
    - Piku: deploys whichever branch you push.
  - Val Town built its own VCS instead of hosting git.

4. Data: defaults and seeding
- Isolated by default (the current trend):
  - Deno Deploy: a logical database per timeline, injected automatically; KV too. But one shared preview DB today.
  - Netlify Database: 'Production deploys are the only ones allowed to access the main database'.
  - Replit: separate dev and prod DBs since 2025.
  - Supabase: 'data-less by default'.
  - Railway: DB services, volumes and buckets are per environment.
  - Render Preview Environments: 'do not copy any data'.
  - Convex: each deployment has its own data.
  - Amplify: a backend per branch, PR or sandbox.
  - Cloudflare Previews: only Durable Objects and containers are automatic.
- Shared by default (older or frontend-oriented):
  - App Engine: 'Cloud Datastore and Memcache are shared between services and versions'.
  - Firebase preview channels: 'interacts with your real backend'.
  - Netlify Blobs: getStore is site-wide.
  - Cloudflare Version URLs.
  - Render service previews: they copy the base service's env, including DB URLs.
  - Dokku apps:clone: env vars, including connection strings, are preserved.
  - Apps Script: script properties are per script.
  - Val Town branches: undocumented; inferred shared.
- Seeding spectrum:
  - Empty plus seed script: Heroku postdeploy; Supabase seed.sql; Render initialization; Convex --preview-run; Fly release_command fixtures.
  - Schema only: PlanetScale's default; Neon schema-only branches.
  - Copy of production: Netlify Database (copy-on-write at creation); Neon's default branch; Supabase 'Include data'; PlanetScale Data Branching (from backup); Val Town 'fork the database too'; Replit's legacy copy.
  - Anonymized copy: Neon.
  - Refresh: Neon 'Reset from parent'; recreating a Supabase branch.
- Flow back to production:
  - Schema moves via migrations at promote or publish time:
    - Netlify Database applies migrations before publish; a failure blocks the publish, and with auto-publish off it waits.
    - Replit diffs the schema at publish and rehearses it on a Neon branch of production.
    - Deno's pre-deploy command runs per timeline.
    - Heroku's release phase runs on promotion.
    - PlanetScale uses deploy requests.
  - Data moves only through narrow, explicit tools. Netlify's 'Data changes' applies inserts and updates in one transaction, never deletes, and is blocked if schemas differ. PlanetScale 'does not provide data syncing'.
- Mapping (inference):
  - main keeps today's resource paths, so there is zero migration (docs/compat.md rule 1, lines 18-23).
  - Each non-primary deployment gets its own namespace, empty by default.
  - Copy, snapshot or copy-on-write is an explicit action that carries a PII warning. Netlify's KB: previews 'can contain PII, and preview links are public'.

5. Secrets: defaults
- The most common default: values are per environment and are not copied into non-prod automatically, or are copied once from an explicit base when the environment is created:
  - Railway sealed variables are 'excluded from environment duplication, PR environments, environment sync diffs'.
  - Render `sync: false` values are not copied into previews.
  - Supabase: 'Secrets set for one branch are not automatically available in other branches'.
  - Heroku review apps get only app.json env plus the pipeline's 'Review app config vars'.
  - The Cloudflare Previews Base is copied at creation; later changes reach only new Previews; a dashboard import requires re-entering secrets.
  - Convex type defaults are copied at creation and 'not kept in sync'.
  - Glitch clears .env values on remix but keeps the names.
- Counterexamples:
  - Netlify gives one value to all contexts unless you split it.
  - Dokku clones and Render service previews copy every variable.
- Change semantics:
  - Vercel and Netlify need a new deploy.
  - Fly `secrets set` restarts the Machines (or use --stage).
  - Azure settings are sticky or swappable, per setting.
- Mapping (inference): vault values keyed by (tile, deployment), with main = today's values. A new deployment starts with names only, as placeholders, or with an explicit copy.

6. Bindings: does non-prod get a parallel fabric?
- Three patterns exist:
  - (a) Resolution inside the environment:
    - Railway: `*.railway.internal` and reference variables resolve only inside the environment ('a misconfigured staging worker… gets the staging API, never the production one').
    - Render: fromDatabase and fromService resolve in-environment, plus 'Block cross-environment connections'.
    - Amplify: a backend per branch, with an opt-in to share dev's.
    - Deno: per-timeline database injection.
    - Wrangler environments: explicit `worker-a-staging` naming.
  - (b) Previews call production:
    - Cloudflare Previews: 'Service bindings from a Preview call the bound Worker's production deployment'. Future work: 'keeping the entire request path inside matching Previews'.
    - Firebase preview channels.
    - Vercel previews call whatever the Preview env points at.
  - (c) Code conventions: App Engine's 'dev projects only call other dev projects'.
- Operator overrides exist wherever this matters:
  - Render `previewValue`, e.g. one shared DB for all previews.
  - Amplify 'share resources across branches'.
  - Cloudflare per-branch binding overrides.
- Per-app grants shared across environments are rare. The closest analogue to xbin's "grants per tile, data per deployment" is Retool: an app uses the same resource objects everywhere, each resource has a configuration per environment, and resource permissions can be set per environment.
- Azure keeps managed identities with the slot, so identity does not travel with code. Neon regenerates role passwords on children of protected branches.
- Mapping (inference): keep grants and bindings per tile; resolve provider data by deployment. For dev→provider calls the evidence points to three options:
  - (1) Call the provider's primary. This is Cloudflare's current default and the simplest, but a dev consumer can then write the provider's production data.
  - (2) Call the provider's same-named deployment when it exists, with an explicit rule when it doesn't (deny, or primary). This is Railway-like and Cloudflare's stated direction.
  - (3) Per-binding override.
- xbin already has a hook for (2)/(3): D32's `iface:<svc>@<tile-glob>[#<inst-glob>]` pins interface allowances to a provider instance ('the dev instance only, for this org'; plans/DECISIONS.md:754-761).

7. Inbound traffic, webhooks and cron
- The near-universal default: custom domains, routes, cron, queue consumers and scheduled jobs target only the primary; non-primary copies are reachable only on their own URL.
  - Vercel: 'invokes cron jobs only for production deployments'.
  - Netlify: scheduled functions 'only run on their schedule for published deploys', with a manual 'Run now' for previews.
  - Cloudflare Previews 'do not take over zone routes, production custom domains, Queue consumers, or other production triggers'.
  - App Engine cron goes to the versions serving traffic and is never split.
  - Azure custom domains and WebJobs schedulers don't swap.
  - Render's tutorial says to opt workers and crons with external side effects out of previews.
- Double-firing wherever background work runs per copy:
  - Azure Functions slots: 'common to disable timer triggered functions in slots to prevent simultaneous executions'. Swap warm-up runs staging with production settings, so timers fire from both slots (azure-functions-host#8999). SQL triggers created duplicate leases (#882).
  - Apps Script triggers run head code.
  - n8n 2.0's production webhook ran drafts (#23549).
  - Railway, Convex and Piku keep crons per environment or app (inference).
- Access to non-primary copies:
  - Apps Script's /dev URL is editors-only.
  - Vercel Standard Protection (the default) protects every URL except production domains.
  - Cloudflare Access protects Version and Preview URLs.
  - An Azure slot at an explicit 0% is reachable only via x-ms-routing-name.
  - Heroku predictable review-app URLs risk subdomain takeover.
- Mapping (inference): incoming interface bindings, ingress, webhooks, bus subscriptions and scheduled work attach only to the primary. Dev is reachable on an explicit per-deployment URL for humans with write or higher on the tile, with a manual "run now" for scheduled handlers.

8. UX: where the controls live and how "which is live" is shown
- Controls:
  - Netlify: 'Stop auto publishing' / 'Start auto publishing' on the Deploys page.
  - Vercel: the Production Deployment tile (Instant Rollback, Undo Rollback) plus the auto-assign toggle in Settings > Environments.
  - Deno: lock and unlock on the Timelines page.
  - Cloudflare: the Deployments tab plus 'Save' vs 'Deploy'; the Production / Previews Base toggle in every settings section.
  - Retool: the Releases and history panel.
  - n8n: the Publish button, which has states.
  - Railway: the staged-changes banner.
  - Azure: the Swap dialog, with source/target setting diffs.
  - Heroku: pipeline 'Promote' buttons.
- Live indicators:
  - Vercel's Staged / Promoted / Current.
  - Retool's 'Live' label.
  - Cloudflare's 'Active Deployment' plus an environment breadcrumb (Production / Previews).
  - n8n 'Published, has changes', plus an instance color 'to know which instance they're in'.
  - Heroku's pipeline shows production running different code than staging.
  - Firebase release history.
  - Azure's `x-ms-routing-name` cookie.
- Mapping (inference): the tile's terminal window shows:
  - the hot-reload state: which deployment follows the working tree, or paused at a snapshot;
  - a 'Reload now' button;
  - 'Promote dev → main';
  - which deployment is primary;
  - a "main has unapplied changes" / "N changes behind" badge.

9. Permission to promote
- Promotion is commonly its own right:
  - n8n `workflow:publish` vs `workflow:update` (drafts only).
  - Convex production is editable only by project/team admins.
  - Render protected environments.
  - Railway environment RBAC (git push can still deploy).
  - Vercel rollback: admins or 'Full Production Deployment'.
  - n8n 'Protected instance'.
  - PlanetScale's optional required approval.
- Mapping (inference): promote, pause and primary reassignment could require the tile's top human level ('terminal') or a new capability, because 'write' today implies live editing.

10. Compatibility precedents
- Retool: live until the first release; Unpublish restores live.
- Val Town: added branches while keeping live saves, and kept user-scoped DBs 'for the long term'.
- n8n 2.0 flipped the default and paid for it:
  - it was BREAKING and needed an auto-migration (active → published);
  - the API default stayed 'publishIfActive=true' 'so existing integrations keep their current behavior';
  - it still shipped a trigger path that bypassed the pin.
- Replit migrated apps to separate DBs, and old remixes that shared a DB needed manual work.
- Mapping to docs/compat.md (inference): the feature must be purely opt-in.
  - The HTTP API is additive (rule 2, lines 24-28).
  - Shipped URLs are frozen, so /c/<tile>/ keeps serving the primary (rule 3, lines 29-35).
  - Manifest keys are additive and unknown keys ignored (rule 7, lines 51-53).
  - SDK semantics change only in the permissive direction (rule 8, lines 54-56).
  - Silent paths warn for one release before they error (rule 11, lines 71-74).
  - Every inbound path (ingress, bindings, bus, timers, frame loads) must resolve through the same "which code is live" pointer. That is the n8n lesson.

11. Terminology
- Avoid these; they collide with existing xbin vocabulary:
  - "identity": the path IS the tile's identity (docs/elements.md:5); the frame token is 'your tile's identity' (docs/frontend-kit.md:23); element identities in auth (plans/DECISIONS.md:24).
  - "slot": interface slots (plans/DECISIONS.md:495, :544).
  - "instance": D32 provider instance (plans/DECISIONS.md:754-761); D8 instance credentials (:306).
  - "deployment": the xbind install ('per-deployment' .xbin/ at BU-2, plans/DECISIONS.md:124; plans/deployment.md). It also means an immutable artifact at Vercel and Netlify.
  - "channel": hub and adapter channels (plans/DECISIONS.md:1849, :2283-2300).
  - "release": xbin release artifacts (plans/DECISIONS.md:1005-1022).
  - "preview": already used for dry-run previews (plans/DECISIONS.md:830, :1370).
  - "fork": `cp -r` forks create a new tile (docs/elements.md:5).
  - "revision": native prop revisions (docs/compat.md:68) and auth save revisions (docs/auth.md:994).
  - "live" as an environment name: it would make 'Live reload' (docs/elements.md:356, :405) read "live reload targets dev".
- Preferred:
  - "environment" for the named per-tile runtime (main, dev). It is industry-standard (Vercel, Railway, Render, Retool; Deno "contexts") and appears only incidentally in DECISIONS.md (4 hits, e.g. :627, :2806).
  - "main" as the default name. It matches git, Val Town, Neon's API name and PlanetScale's production branch.
  - "primary" as a reassignable role, not a name. Precedents: Neon 'default branch', Deno 'production timeline', Azure 'production slot'.
  - "snapshot" or "version" for the immutable code state.
  - "promote" for copying tested code A→B without rebuilding: Heroku, Vercel, Cloudflare, PlanetScale.
  - "deploy to" for sending the working tree to a chosen environment.
  - "reload now" / "publish" for activating a pending build on a paused environment: Netlify 'Publish deploy', Retool, n8n.
  - "pause" / "resume" auto-reload: Netlify 'stop/start auto publishing', Deno 'lock/unlock'.
  - "hot reload follows <env>" for the attachment, as in Cloud Run's LATEST.

12. Pitfalls platforms hit
- Previews sharing the production DB: Render service previews; Cloudflare Version URLs ('use production resources', now discouraged for PR testing); Vercel staged production builds; Railway PR environments copying a literal production DATABASE_URL; Firebase preview channels; App Engine versions; Dokku apps:clone; Replit's legacy shared DB and remixes.
- Destructive operations from previews: a Netlify preview's `deleteAll()` wipes production Blobs, and locking doesn't protect it.
- PII leakage from copying production: public Netlify preview links on DB branches. Hence Heroku's refusal to copy DBs, Supabase's data-less default, and Neon's schema-only and anonymized branches.
- Cron and webhooks double-firing or running the wrong code: Azure slot timers and swap warm-up; Azure SQL trigger duplicate leases; the n8n webhook running drafts; Apps Script triggers running head code; App Engine cron ignoring splits; Railway, Convex and Piku crons per copy (inference).
- Secrets leaking or going stale: copied wholesale (Dokku clones, Render service previews, Netlify's single default value); copied once and drifting (Cloudflare Previews Base, Convex defaults); readable by anyone who can deploy (Fly's warning).
- Slot-swap data surprises: non-sticky connection strings travel with code; warm-up runs with production settings; managed identities stay behind; a new slot copies all settings regardless of stickiness.
- Rollback doesn't roll back state: Heroku (add-ons, externally stored state), Fly (config and secrets), Vercel (env vars stay; external APIs and DBs), Cloudflare (storage isn't versioned). PlanetScale's revert works only within 30 minutes.
- Promotion carrying the wrong config: Lambda, Heroku stateful builds, Vercel rebuilds, the Deno Classic KV bug.
- Pause and rollback interplay: rollback without pause gets undone by the next push (Vercel's fix); Cloud Run's sticky pin surprises users; resume semantics must be explicit (Netlify).
- Version skew during cutover or splits: Cloudflare HTML/asset 404s (fixed by version affinity); App Engine cache warnings.
- Undocumented semantics: Val Town doesn't say whether branches share data or fire triggers; Vercel's own docs contradict each other on cron after rollback. xbin's design docs should state these explicitly for every inbound path and resource kind.

## Platforms

### Cloudflare Workers (Versions & Deployments, Version URLs, Wrangler environments, Worker Previews; plus Cloudflare Pages)

**Model.** The unit is a Worker. Every code or config change creates a Version: 'the complete state of your Worker at a point in time: its bundled code, static assets, bindings, and compatibility settings' (unique ID, optional message and tag). A Deployment 'determines which version(s) of your Worker are actively serving traffic': either one version at 100%, or two versions split during a gradual deployment. `wrangler deploy` does both in one step (a new version at 100%). The decoupled flow is `wrangler versions upload` (version only) followed by `wrangler versions deploy` (interactive percentages). In the dashboard, the editor's Deploy dropdown has 'Save' (create a version without deploying), and Deployments has 'Promote deployment'. Only the last 100 uploaded versions can be deployed; rollbacks revert to a previously deployed version. To smoke test, deploy the new version at 0% and reach it with the `Cloudflare-Workers-Version-Overrides` header. This works only for versions in the current deployment, and only on fetch-based service-binding calls, not RPC. The Compare workflows page (Sept 2026) lists three coexisting non-prod workflows. (1) Version URLs, renamed from 'preview URLs': a per-version URL `<version-prefix>-<worker>.<subdomain>.workers.dev`, plus aliases via `wrangler versions upload --preview-alias staging`. They run that version with its production configuration and resources. (2) Wrangler environments: an `env.<name>` block deploys a separately named Worker `<name>-<env>`, and the root Worker is a separate deployment too. (3) Worker Previews (launched 2026-09-22, Wrangler >= 4.135.0): `npx wrangler preview` creates or updates an isolated environment for the current git branch under the same Worker. The name defaults to the branch, and Workers Builds creates Previews on push and posts the URLs to PRs. Each Preview has its own vars, secrets, bindings, URL and observability. Its 'Preview URL' always serves the branch's latest deployment; its 'Deployment URL' is immutable per deploy. A `previews` block (the 'Previews Base') defines what new Previews start from, and each branch can override it. Long-lived staging/QA/per-developer Previews are listed as future work. Cloudflare Pages (the older product) has a production branch plus preview deployments. Each preview gets a hash URL plus a branch alias `<branch>.<project>.pages.dev` that tracks the branch's latest commit. Config overrides exist only as `env.production` and `env.preview`, and 'Pages does not currently support branch-based configuration'.

**Terms.**

- Version — immutable snapshot of code, assets, bindings and compatibility settings, created by every change
- Deployment — the version(s) serving traffic: one at 100% or two split
- Active Deployment — dashboard label for the deployment currently serving
- Gradual deployment — percentage split of traffic between two versions
- Version overrides — `Cloudflare-Workers-Version-Overrides` header that routes a request to a specific version in the current deployment
- Version affinity — `Cloudflare-Workers-Version-Key` header, hashed to pin a user to one version for the duration of a split
- Version URL (formerly preview URL) — per-version workers.dev URL that runs with production resources; aliased Version URL via --preview-alias
- Wrangler environment — env.<name> config block deployed as a separate Worker named <name>-<env>
- Non-inheritable keys — bindings, vars and secrets that must be redeclared in each environment
- Preview (Worker Previews) — per-branch isolated environment under the same Worker (`npx wrangler preview`)
- Previews Base — base configuration (vars, bindings, secrets) copied into each new Preview
- Preview URL / Deployment URL — the branch-latest URL vs the immutable per-deploy URL of a Preview
- preview_id — KV binding field used by `wrangler dev --remote` so development does not touch the production namespace
- Pages branch alias — <branch>.<project>.pages.dev, always pointing at that branch's latest preview deployment

**Data.** Versions do not carry state: 'State changes for associated storage resources such as KV, R2, Durable Objects, and D1 are not tracked with versions.' Version URLs share production data by design. In Wrangler environments, storage bindings are non-inheritable, so each environment names its own KV/D1/R2 IDs (Cloudflare's best-practice example: dev-kv-id / staging-kv-id / prod-kv-id). A Durable Object binding inside an environment points at a different Worker code name 'and will therefore access different Durable Objects with different persistent storage', unless `script_name` points back at the top-level Worker. Worker Previews: Durable Object namespaces and container apps are provisioned automatically per Preview. That state persists across the Preview's deployments and is deleted with the Preview. Account-level resources (KV, D1, R2, queues, Vectorize, Hyperdrive, Analytics Engine, Pipelines, Workflows, Secrets Store…) are shared or isolated purely by which ID is bound: 'Two Previews bound to the same account-level resource ID or name share its data or instances.' The recommended D1 pattern is one shared staging database, bound in the base branch's `previews.d1_databases`. A branch overrides it when it needs isolation, and migrations are applied through a separate migrations config. There is no copy/seed primitive: new DO namespaces start empty, and otherwise a Preview sees whatever the bound staging resource holds. Local `wrangler dev` defaults to local KV 'to avoid interfering with any of your live production data'; `--remote` uses the binding's `preview_id`.

**Bindings and secrets.** Wrangler environments: bindings, vars and secrets are non-inheritable, and a service binding to another Worker's environment must name it explicitly (`service = worker-a-staging`). Previews do NOT inherit production settings. The `previews` block is required but may be empty. A binding used in code but missing from `previews` does not exist in the Preview (error 1101), and Wrangler warns when bindings are missing. 'Service bindings from a Preview call the bound Worker's production deployment' ('the Preview of Worker A can only bind to the production Worker B. It does not automatically bind to a matching Preview of Worker B'). Same-Worker calls can stay inside the Preview via `ctx.exports`. Workflow bindings run the existing Workflow's deployed code. The launch blog lists as future work 'keeping the entire request path inside matching Previews'. Secrets cannot live in the config file. They are set once in the Previews Base (`wrangler preview base-config secret put`); 'Each new Preview receives those secrets when it is created… Later changes to Base secrets apply only to new Previews, so active Previews remain unchanged.' A single Preview can override a secret with `wrangler preview secret put --name`. When importing production settings in the dashboard, 'You must re-enter secrets manually.' Pages: bindings and env vars are configured separately for Production and Preview.

**Inbound (traffic, webhooks, cron).** Production routes, custom domains, Cron Triggers and Queue consumers target production only. Previews 'do not take over zone routes, production custom domains, Queue consumers, or other production triggers'. The docs say not to put Queue consumers, Cron Triggers or production routes into `previews`, and to test scheduled logic through a test-only route that calls the same function. Previews are reachable on workers.dev or on custom-domain Preview hostnames (e.g. feature-login.previews.example.com), 'so that auth providers, cookies, … CORS, and OAuth redirects work the same way they will in production'. Version URLs are public when enabled (the default follows `workers_dev`), and Cloudflare Access can protect Version and Preview URLs. Version URLs are not generated for Workers that implement a Durable Object. During a gradual deployment each request is routed independently unless version affinity is set. Cloudflare documents the resulting version skew: HTML from version A, then a content-hashed asset request routed to version B, which returns 404.

**UX.** Dashboard Worker > Deployments lists versions, the Active Deployment and 'Promote deployment'. The code editor's Deploy button has a 'Save' option (version without deploying). The Worker > Settings sections (Variables and secrets, Bindings, Observability, Runtime) each have toggles for 'Production' and 'Previews Base'. An environment breadcrumb next to the Worker name switches between Production and every Preview. Each Preview has its own logs, errors, metrics and traces. The Version URL toggle lives under Settings > Domains & Routes. Wrangler environments appear as separate Workers.

**Lessons.** (1) Cloudflare renamed 'preview URLs' to 'Version URLs' and now says 'Do not use Version URLs for branch or pull request testing' because they 'use production resources'. That is a named, retired pitfall. (2) The replacement defaults to non-inheritance: a Preview gets nothing from production unless it is declared, and missing bindings fail loudly. (3) Only state the Worker itself owns (Durable Objects, containers) is isolated automatically. Shared account-level resources are isolated only by rebinding them, and the recommended middle ground is one shared staging store for all Previews. (4) Cross-Worker preview fabric is not solved yet: previews call production dependencies. (5) Base secrets are copied once at creation, not synced, so existing Previews drift.

**Sources.**

- <https://developers.cloudflare.com/workers/versions-and-deployments/>
- <https://developers.cloudflare.com/workers/versions-and-deployments/deployment-management/>
- <https://developers.cloudflare.com/workers/versions-and-deployments/gradual-deployments/>
- <https://developers.cloudflare.com/workers/versions-and-deployments/gradual-deployments/version-affinity/>
- <https://developers.cloudflare.com/workers/configuration/versions-and-deployments/version-overrides/>
- <https://developers.cloudflare.com/workers/versions-and-deployments/version-urls/>
- <https://developers.cloudflare.com/workers/previews/>
- <https://developers.cloudflare.com/workers/previews/compare-workflows/>
- <https://developers.cloudflare.com/workers/previews/get-started/>
- <https://developers.cloudflare.com/workers/previews/configuration/>
- <https://developers.cloudflare.com/workers/previews/resources/>
- <https://developers.cloudflare.com/changelog/post/2026-09-22-worker-previews/>
- <https://blog.cloudflare.com/worker-previews/>
- <https://developers.cloudflare.com/workers/wrangler/environments/>
- <https://developers.cloudflare.com/workers/best-practices/workers-best-practices/>
- <https://developers.cloudflare.com/durable-objects/reference/environments/>
- <https://developers.cloudflare.com/kv/concepts/kv-bindings/>
- <https://developers.cloudflare.com/pages/functions/wrangler-configuration/>
- <https://developers.cloudflare.com/pages/functions/bindings/>
- <https://developers.cloudflare.com/pages/configuration/preview-deployments/>

### AWS Lambda (versions, $LATEST, aliases)

**Model.** The unit is a function. `$LATEST` is the only mutable, unpublished version: 'any time you deploy your function code, you overwrite the current code in $LATEST'. `PublishVersion` snapshots $LATEST into an immutable numbered version. Numbers increase monotonically and are never reused, even after delete/recreate, and publishing with no change is a no-op. The snapshot covers the code and most configuration, explicitly including environment variables, runtime, handler, layers, memory, timeout, VPC and IAM role. Settings that stay editable on a published version: triggers, destinations, provisioned concurrency, async invocation and DB connections/proxies. An alias is 'a pointer to a function version that you can update', with its own ARN. It can point only to a version, never to another alias, and `update-alias` is the promote/rollback operation. A weighted alias (`RoutingConfig.AdditionalVersionWeights`) splits invocations between at most two versions. Both must share the execution role and DLQ config, and both 'must be published. The alias cannot point to $LATEST'. A qualified ARN has a :version or :alias suffix; an unqualified ARN implicitly means $LATEST. An alias cannot be created from an unqualified ARN.

**Terms.**

- $LATEST — the single mutable head; every code deploy overwrites it
- Version — immutable, numbered snapshot of code plus configuration (including environment variables)
- Publish — create a version from $LATEST
- Alias — named, updatable pointer to one version, with its own ARN (e.g. PROD, live)
- Weighted alias / alias routing configuration — canary split between two published versions
- Qualified ARN — function ARN with a :version or :alias suffix
- Unqualified ARN — ARN without a suffix; invokes $LATEST

**Data.** There is no built-in data plane; separation is by configuration. Environment variables are frozen into the published version, and an alias only selects a version, so an alias cannot carry configuration of its own. Promoting version N from a 'dev' alias to 'prod' therefore carries N's environment, including its database endpoints. (Inference from the versioning docs and the re:Post note that a published version's environment variables cannot be changed.) Per-stage data separation is therefore usually done with separate functions, stacks or accounts, or by code that resolves configuration from the invoked alias (inference: common practice). There is no seeding concept.

**Bindings and secrets.** Event source mappings and resource-based policies can name an alias ARN, so they need no update when the alias moves. A permission can be scoped to an alias, a version or the whole function. With an alias-scoped policy, 'If you attempt to invoke the function without an alias or a specific version, then you get a permission error. This permission error still occurs even if you attempt to directly invoke the function version associated with the alias.' The IAM execution role is part of each version, so both halves of a weighted alias must share it. Secrets and environment variables are per version; there are no alias-scoped secrets (inference).

**Inbound (traffic, webhooks, cron).** Every trigger targets either a qualified ARN (alias or version) or the unqualified ARN ($LATEST). An alias's weights apply to every invocation through that alias, including event-source mappings. In the cited Stack Overflow thread, a DynamoDB-stream mapping had to target the alias ARN before the split applied. A trigger on the unqualified ARN follows $LATEST, i.e. it picks up every deploy.

**UX.** Console: function > Versions tab 'Publish new version'; Aliases > 'Create alias', with a 'Weighted alias' expander. The alias list (alias → version, weights) is the only 'which is live' view; API: CreateAlias/UpdateAlias/DeleteAlias.

**Lessons.** The cleanest separation of a mutable head from named pointers: $LATEST ≈ working tree, aliases ≈ named deployments. Two limits worth avoiding. First, configuration frozen into the artifact means environments cannot differ per pointer, so promotion drags dev configuration along. Second, the mutable head cannot take part in weighted routing.

**Sources.**

- <https://docs.aws.amazon.com/lambda/latest/dg/configuration-versions.html>
- <https://docs.aws.amazon.com/lambda/latest/dg/configuration-aliases.html>
- <https://docs.aws.amazon.com/lambda/latest/dg/configuring-alias-routing.html>
- <https://docs.aws.amazon.com/lambda/latest/dg/using-aliases.html>
- <https://docs.aws.amazon.com/lambda/latest/api/API_CreateAlias.html>
- <https://repost.aws/knowledge-center/lambda-alias-new-function-version>
- <https://stackoverflow.com/questions/56140251/how-to-set-up-a-lambda-alias-with-the-same-event-source-mapping-as-the-latest-un>

### Google App Engine (services, versions, traffic split)

**Model.** App (one per project) → services (formerly modules) → versions → instances. Each deploy of a service creates a version. Versions coexist, and each is addressable at `https://VERSION-dot-SERVICE-dot-PROJECT_ID.REGION_ID.r.appspot.com`. Traffic moves by 'migrating traffic' (all to one version) or 'splitting traffic' (`gcloud app services set-traffic SERVICE --splits V1=W1,V2=W2 --split-by ip|cookie`). Splitting applies only to URLs that do not name a version. dispatch.yaml maps URL/host patterns to services. Multiple versions exist 'to quickly switch between different versions of that app for rollbacks, testing, or other temporary events'.

**Terms.**

- Service — independently deployed component of the app (the default service)
- Version — deployed code and config of a service, individually addressable via a -dot- URL
- Instance — running process of a version
- Migrate traffic — send all of a service's traffic to one version
- Split traffic — percentage split by sender-IP hash (0–999) or the GOOGAPPUID cookie (0–999), cookie being more precise
- dispatch.yaml — URL/host → service routing; overrides a cron job's target
- cron.yaml target — the service whose traffic-serving versions receive the cron request

**Data.** There is no per-version data: 'Cloud Datastore and Memcache are shared between services and versions' (namespaces are only a developer pattern); 'Other Google Cloud services, for example Datastore, are shared across your App Engine app'. Services in one project also share a memcache instance and task queues. Google's answer for environments is separate projects: 'it's absolutely essential that the data in the different environments stay isolated. Using multiple Google Cloud projects suits these requirements perfectly.' There is no seeding tooling.

**Bindings and secrets.** All versions of a service run inside the same project, with the project's shared services (inference). For environments split across projects, Google says 'You will need to use code patterns to ensure that the dev projects only call other dev projects and the prod projects only call other prod projects'. That is a manual parallel binding fabric.

**Inbound (traffic, webhooks, cron).** Cron requests go to the target service and 'are routed to the versions in the specified service that are configured for traffic'. Older docs: 'if the default version of the module changes, the job will run in the new default version'. Cron is not split: IP splitting always hits the same version, and cookie splitting has no cookie to use. dispatch.yaml overrides a cron target, and delivery is at-least-once, so handlers must be idempotent. Internal requests from Google infrastructure come from a few IPs, so under IP splitting they may all land on one version. Non-serving versions stay reachable at their version URL.

**UX.** Console Versions page: select versions → 'Split traffic', then choose the method and percentages; the traffic-allocation column shows which versions serve.

**Lessons.** Many live code copies with zero data isolation: the canonical 'test version writes production data' setup. Google's own docs resolve it by prescribing a project per environment plus code conventions for dev→dev calls. Background work follows only the serving versions. Splitting comes with caching and version-skew warnings (rename changing static assets per version, or use Vary: Cookie).

**Sources.**

- <https://cloud.google.com/appengine/docs/standard/splitting-traffic>
- <https://cloud.google.com/appengine/docs/standard/scheduling-jobs-with-cron-yaml>
- <https://docs.cloud.google.com/appengine/docs/an-overview-of-app-engine>
- <https://cloud.google.com/appengine/docs/legacy/standard/python/microservices-on-app-engine>
- <https://cloud.google.com/appengine/docs/legacy/standard/python/creating-separate-dev-environments>
- <https://cloud.google.com/appengine/docs/standard/communicating-between-services>
- <https://cloud.google.com/appengine/docs/legacy/standard/python/config/appref>

### Google Cloud Run (revisions, LATEST, tags) — extra, directly relevant to 'follow vs pin'

**Model.** A service is made of immutable revisions: 'When you deploy to a service or change the configuration of a service, an immutable revision is created.' Traffic is assigned to revisions by percent; the special target LATEST is 'the latest ready revision'. By default traffic follows LATEST, but 'If you split traffic between multiple revisions or assigned traffic to a previous revision, all subsequent deployments use that traffic split pattern going forward.' `gcloud run deploy --no-traffic` binds LATEST's traffic to the current revision, so the new revision gets none, and 'After a deployment with this flag the LATEST revision will not receive traffic on future deployments' until `gcloud run services update-traffic --to-latest`. The console equivalent is the 'Serve this revision immediately' checkbox. Revision tags give a revision its own URL (`https://TAG---SERVICE-HASH.a.run.app`) regardless of its traffic share. A tag can point at LATEST (`--set-tags=candidate=LATEST`), and traffic can be moved by tag (`--to-tags green=5`).

**Terms.**

- Revision — immutable snapshot of a service's code and config
- Traffic target — a revision (or LATEST) with a percent
- LATEST — the latest ready revision; traffic or tags bound to it follow new deploys
- Tag — name that gives a revision its own URL, independent of traffic
- --no-traffic — deploy without traffic and stop auto-following LATEST
- --to-latest — resume sending 100% to the latest revision on every deploy
- Serve this revision immediately — console checkbox that overrides any split

**Data.** None built in: all revisions reach whatever resources their configuration names, since config is part of each revision. Revisions not receiving requests consume no resources and are not billed (unless min instances are set).

**Bindings and secrets.** Environment variables and secrets are part of each revision (`--set-env-vars`, `--set-secrets` at deploy). A tagged test revision therefore reaches production resources unless it was deployed with different values (inference).

**Inbound (traffic, webhooks, cron).** The service URL and custom domains follow the traffic percentages; tagged URLs reach one revision directly. A revision cannot be deleted while it can receive traffic, while it is the service's only revision, or while it is the latest revision.

**UX.** Console 'Manage traffic' form; the revision list shows traffic % and tags; the deploy form has the 'Serve this revision immediately' checkbox.

**Lessons.** The best prior art for a sticky 'auto-follow' flag. Following LATEST is the default; any manual pin silently turns following off for all future deploys (a documented surprise); `--to-latest` turns it back on. A tag bound to LATEST is exactly 'a dev URL that follows every new build while the primary stays pinned'.

**Sources.**

- <https://docs.cloud.google.com/run/docs/rollouts-rollbacks-traffic-migration>
- <https://docs.cloud.google.com/sdk/gcloud/reference/run/services/update-traffic>
- <https://docs.cloud.google.com/sdk/gcloud/reference/beta/run/deploy>
- <https://docs.cloud.google.com/run/docs/managing/revisions>

### Google Apps Script (head deployment vs versioned deployments)

**Model.** A project's code is autosaved. A Version is 'a static snapshot… Once created, a version is immutable… a save point'. A Deployment is 'a release that makes a specific version of your script available for users', with a unique URL or ID. There are two types. The Head deployment is created automatically for every project and 'always syncs with the most recently saved code'; it is for testing: 'Use head deployments to test code. Don't use head deployments for public use.' A Versioned deployment is connected to one version. To ship, 'create a new version and then edit the deployment to point to that new version. This updates the application for all users while maintaining the same URL or deployment ID.' Multiple versioned deployments can be active at once. Versioned deployments can't be deleted, and a version in use by an active deployment can't be deleted. Web apps use `/exec` for a versioned deployment and `/dev` for the head: '/dev… can only be accessed by users who have edit access to the script. This instance of the app always runs the most recently saved code.' Libraries can be consumed at 'HEAD (Development Mode)'.

**Terms.**

- Head deployment — always-latest saved code; for testing; editors only
- Versioned deployment — pinned to a version; stable URL/ID; retargeted to newer versions
- Version — immutable code snapshot ('save point')
- Test deployments — Deploy menu entry that exposes head URLs for web apps/add-ons
- /dev vs /exec — head vs versioned web-app URL
- Development mode (HEAD) — consuming a library at its head
- executeAs / access — per-deployment web-app settings (USER_ACCESSING vs USER_DEPLOYING; MYSELF/DOMAIN/ANYONE/ANYONE_ANONYMOUS)

**Data.** The Properties service is per script: 'Properties are never shared between scripts'. Script properties are 'shared among all users of a script, add-on, or web app'. There is no per-deployment store, so head and versioned deployments share script properties, cache and lock (inference from that scoping).

**Bindings and secrets.** Who a web app runs as (`executeAs`) and who may call it (`access`) are per-deployment settings in the manifest/entry point. Installable triggers 'always run under the account of the person who created them'.

**Inbound (traffic, webhooks, cron).** Background execution does not follow deployment pins cleanly. A trigger created programmatically with ScriptApp.newTrigger runs the latest saved (head) code; a manually created trigger can 'Choose which deployment should run' (Stack Overflow). A Google Issue Tracker report says a trigger created against a deployed version was automatically moved to a newly deployed version, and Google answered that there are 'no plans to change this'.

**UX.** Deploy menu: New deployment / Manage deployments / Test deployments. Manage deployments lists the deployments with their version numbers and descriptions, and is where you edit a deployment to a new version.

**Lessons.** The closest vocabulary to xbin's split: an always-on 'head' that follows saves (visible only to editors), plus named, stable deployments retargeted to immutable versions. Pitfalls: triggers do not honour deployment pins, and head code shares state (script properties) with the pinned public deployment.

**Sources.**

- <https://developers.google.com/apps-script/concepts/deployments>
- <https://developers.google.com/apps-script/guides/web>
- <https://developers.google.com/apps-script/guides/properties>
- <https://developers.google.com/apps-script/guides/libraries>
- <https://developers.google.com/apps-script/guides/triggers/installable>
- <https://developers.google.com/apps-script/manifest/web-app-api-executable>
- <https://developers.google.com/apps-script/api/reference/rest/v1/projects.deployments>
- <https://stackoverflow.com/questions/54210044/how-to-use-versioned-deployments-when-programmatically-creating-triggers>
- <https://issuetracker.google.com/issues/145770681>

### Vercel (preview vs production deployments, custom environments, promote, instant rollback)

**Model.** Deployments are immutable builds with generated URLs. There are three default environments (Local Development, Preview, Production), plus Custom Environments on Pro/Enterprise (e.g. staging, QA) with branch tracking, a domain and their own variables. A preview deployment is created by a push to a non-production branch, by a PR, or by `vercel` without `--prod`; production comes from the production branch or `vercel --prod`. 'The first deployment of a new project is always a production deployment.' Preview URLs come in two forms: a branch-specific URL (latest on the branch) and a commit-specific URL. Production deployment states: Staged (pushed but no domain auto-assigned), Promoted, and Current ('aliased to your domain… currently being served'). There are three ways to change what production serves. (1) Instant Rollback reassigns the domains to an earlier deployment that already served production, without a rebuild. (2) 'Promote preview to production' does a complete rebuild with production env vars. (3) 'Promote a staged production build' needs no rebuild but requires auto-assignment to be off. The switch is the 'Auto-assign Custom Production Domains' toggle, under Settings > Environments > Production > Branch Tracking. When it is off, production deployments wait for a manual Promote. 'After a rollback, Vercel turns off auto-assignment of production domains… new pushes to your production branch won't replace the rolled-back deployment'; 'Undo Rollback' or `vercel promote` re-enables it. Rolling Releases provide gradual production rollouts.

**Terms.**

- Preview deployment — any non-production deployment (branch, PR, CLI without --prod)
- Production deployment — built from the production branch or --prod, with Production env vars
- Custom Environment — named pre-production environment (staging/QA) with branch tracking, a domain and variables
- Branch-specific URL / commit-specific URL — branch-latest vs exact-commit preview URL
- Auto-assign Custom Production Domains — toggle; when off, production deployments are Staged until promoted
- Staged / Promoted / Current — production deployment states
- Instant Rollback — reassign production domains to a previously Current deployment (no rebuild)
- Promote — make a staged or preview deployment Current
- Deployment Protection — access control on deployment URLs (Standard Protection = everything except production domains)
- Sensitive environment variable — value not readable after creation; default for production/preview/custom targets in the CLI

**Data.** Vercel has no platform data store in this model; environment variables decide which database each environment uses. Vercel's own warning: 'Staged production deployments use production environment variables, so testing can access production services and data. Use a custom environment or preview branch with staging credentials when you need separate resources.' Database integrations create per-preview branches: 'The Neon-Managed Vercel integration also creates preview deployment branches from your project's default branch' (Neon docs). Rollback caveat: the rollback dialog reminds you about 'the changing behavior of external APIs, databases, and CMSes used in the current or previous deployments'.

**Bindings and secrets.** Environment variables are scoped per environment (Production, Preview, Development, custom). A Preview variable can be branch-specific; 'Any branch-specific variables will override other preview environment variables with the same name', so a branch declares only what differs. 'Changes to environment variables are not applied to previous deployments… You must redeploy.' `vercel env add` defaults to sensitive for production, preview and custom targets; development variables cannot be sensitive. A custom environment can 'import variables from another environment' ('review the values for your staging services'). Promoting a preview to production swaps in production variables: 'You cannot use your preview environment variables in a production deployment.' Vercel Connect token access and trigger forwarding can be scoped to a Custom Environment, which is connector/webhook scoping per environment.

**Inbound (traffic, webhooks, cron).** Cron: 'Vercel invokes cron jobs only for production deployments and not for preview deployments' (an HTTP GET to the production deployment URL). Users complain there is no way to test cron on previews. After a rollback the docs conflict. The Instant Rollback page says cron jobs 'will be reverted to the state of the rolled back deployment'. The Managing Cron Jobs page says that after an Instant Rollback cron jobs 'will not be updated. They will continue to run as scheduled until they are manually disabled or updated.' Production domains attach only to the Current deployment. Custom aliases are not carried by a rollback unless they were on the previous production deployment. New projects default to Standard Protection, which protects preview URLs and deployment URLs but not production domains.

**UX.** The project overview's 'Production Deployment' tile shows the Current deployment, with Instant Rollback and, when rolled back, an 'Undo Rollback' button. The Deployments list's ⋮ menu has 'Promote' / 'Promote to Production'. Settings > Environments holds Branch Tracking and the auto-assign toggle. PR comments carry preview links. Rollback permission: Owners/Members/Developers on Pro/Enterprise, project administrators, or holders of the 'Full Production Deployment' permission.

**Lessons.** (1) The auto-assign toggle is Vercel's 'pause auto-publish', and rollback engages it implicitly: a rollback must pause auto-deploy, or the next push undoes it. (2) Promotion semantics differ by source (rebuild vs reassign) because configuration is baked in at build time, which confuses users; separate 'which code' from 'which config'. (3) Previews are safe only if users actually give the Preview environment different values; Vercel itself warns that staged production builds hit production data. (4) The docs contradict each other on cron after a rollback, which shows how subtle 'which deployment owns background triggers' is.

**Sources.**

- <https://vercel.com/docs/deployments/environments>
- <https://vercel.com/docs/deployments/promoting-a-deployment>
- <https://vercel.com/docs/instant-rollback>
- <https://vercel.com/docs/environment-variables>
- <https://vercel.com/docs/environment-variables/manage-across-environments>
- <https://vercel.com/docs/environment-variables/managing-environment-variables>
- <https://vercel.com/docs/cli/env>
- <https://vercel.com/docs/cron-jobs>
- <https://vercel.com/docs/cron-jobs/quickstart>
- <https://vercel.com/docs/cron-jobs/manage-cron-jobs>
- <https://vercel.com/kb/guide/troubleshooting-vercel-cron-jobs>
- <https://github.com/vercel/community/discussions/2215>
- <https://vercel.com/docs/deployment-protection>
- <https://vercel.com/changelog/set-team-wide-defaults-for-deployment-protection>
- <https://neon.com/docs/manage/branches>

### Netlify (deploy previews, branch deploys, locked deploys / stop auto publishing, Blobs, Netlify Database)

**Model.** A site/project has atomic deploys. Deploy types: Production deploy (from the production branch; 'If auto publishing is enabled, each new production deploy will become the published deploy'); Deploy Preview (per PR/MR at `deploy-preview-42--site.netlify.app`, also created by agent runs); Branch deploy; deploy permalinks. The published deploy is the one served at the site's main URL. Rollback is 'Publish Deploy' on any earlier atomic deploy ('doesn't trigger a new deploy… Rollbacks are instantaneous'). Locked deploys: 'pinning a site to the latest published deploy for the time being. New deploys won't be published to the main site, although Netlify will still build them and they will be ready for whenever you want to publish them.' The UI is Deploys > 'Lock to stop auto publishing'; the button toggles between 'Stop auto publishing' and 'Start auto publishing'. After unlocking, 'previously built deploys will not auto-publish… You'll need to choose which of your new deploys you want to publish.' 'Stop builds' is separate and halts production builds, Deploy Previews and branch deploys alike. On a Git-connected site, the next push replaces a hand-shipped `netlify deploy --prod` unless the deploy is locked. Retention is 30/90/up to 365 days; the published deploy is never auto-deleted.

**Terms.**

- Deploy context — production, deploy-preview, branch-deploy, preview-server, dev (Local development), plus Branch values (wildcards like release/*)
- Production deploy — built from the production branch
- Published deploy — the deploy served at the main URL
- Deploy Preview — per-PR deploy at deploy-preview-N--site.netlify.app
- Branch deploy — per-branch deploy
- Auto publishing / Locked deploy — 'Stop auto publishing' pins the published deploy; builds continue
- Publish deploy — manual publish or rollback of any atomic deploy
- Stop builds — halt all building (production, previews, branch deploys)
- Site-wide store vs deploy-specific store — Netlify Blobs getStore vs getDeployStore
- Database branch — per-preview copy of the Netlify Database
- Data changes — tab that applies selected branch rows to production

**Data.** Netlify Blobs: stores opened with `getStore(name)` 'are shared across all deploys of your site… This also means you can test your Deploy Previews with production data… be careful to avoid scenarios such as a branch deploy deleting blobs that your published deploy depends on'. `getDeployStore()` is scoped to one deploy, kept in sync on rollback, and cleaned up when the deploy is deleted. Build plugins and file-based uploads may write only to deploy-specific stores 'so that a deploy that fails cannot overwrite production data'. A Sept 2026 KB article, 'Why your deploy preview can delete Netlify Blobs data', warns that `deleteAll()` from a preview wipes the production store, that locking does not protect stores, and suggests `getStore(`uploads-${context.deploy.context}`)`. Netlify Database (Postgres, 2026): 'Production deploys are the only ones allowed to access the main database'; 'Deploy previews get their own database branch, with a copy of the production data taken when the deploy preview is first created'; every agent run gets its own branch. Migrations are committed files applied by the deploy. For production they run immediately before publish, and a failure blocks the publish; with auto-publish off, Netlify waits for the manual publish before applying them. For previews they run on every deploy. The 'Data changes' tab compares a branch against a temporary production snapshot and applies selected inserts/updates to production in one transaction. It never deletes production rows and is blocked when schemas differ. The KB warns: 'previews can contain PII, and preview links are public.'

**Bindings and secrets.** An environment variable has a scope (Builds, Functions, …) and per-deploy-context values: Production, Deploy Previews, Branch deploys, Preview server, Local development, plus Branch values (wildcards allowed). 'By default, environment variables are available to all scopes and have the same value for all deploy contexts.' Values are captured at deploy time: 'To apply updated environment variable values, create a new deploy.' Local development values cannot be marked secret. Shared (team) variables are overridden by site variables.

**Inbound (traffic, webhooks, cron).** 'Scheduled functions only run on their schedule for published deploys — Deploy Previews and branch deploys won't trigger them automatically.' Previews get a manual 'Run now' button, and scheduled functions don't work with Split Testing. The published deploy owns the main URL; previews live on their own subdomains.

**UX.** The Deploys page shows the published deploy and the 'Stop auto publishing' / 'Start auto publishing' (Lock/Unlock) button; each deploy's detail page has 'Publish deploy'; branches and contexts are configured under Branches and deploy contexts. Netlify ships a video, 'Pause auto-publishing on Netlify', aimed at users of AI tools ('what if you're not ready to go live yet?').

**Lessons.** (1) The locked deploy is the closest analogue to 'pause hot reload + reload now'. Building continues, publishing pauses, any ready deploy can be published, and unlocking does not flush the backlog. (2) Netlify got burned on data scoping: site-wide Blobs, the default, are shared across contexts, so a preview can destroy production data. Its newer Database product flips the default (the main DB is prod-only; each preview gets its own branch copy). (3) Seeding previews with a copy of production leaks PII through public preview links. (4) Data can be promoted back only through a narrow, explicit tool (inserts/updates, one transaction).

**Sources.**

- <https://docs.netlify.com/deploy/manage-deploys/manage-deploys-overview/>
- <https://docs.netlify.com/deploy/deploy-overview/>
- <https://docs.netlify.com/build/configure-builds/stop-or-activate-builds/>
- <https://www.netlify.com/blog/2021/12/04/controlling-auto-publishing-for-coordinated-releases/>
- <https://developers.netlify.com/videos/pause-auto-publishing-on-netlify/>
- <https://www.netlify.com/knowledge-base/how-to-deploy-a-site-to-netlify/>
- <https://docs.netlify.com/deploy/deploy-types/deploy-previews/>
- <https://docs.netlify.com/build/environment-variables/overview>
- <https://docs.netlify.com/build/functions/environment-variables>
- <https://docs.netlify.com/build/functions/scheduled-functions/>
- <https://docs.netlify.com/storage/blobs/overview/>
- <https://www.netlify.com/knowledge-base/why-your-deploy-preview-can-delete-netlify-blobs-data/>
- <https://www.netlify.com/knowledge-base/how-to-store-files-and-objects-with-netlify-blobs/>
- <https://docs.netlify.com/build/data-and-storage/netlify-database/>
- <https://docs.netlify.com/build/data-and-storage/netlify-database/migrations/>
- <https://docs.netlify.com/build/data-and-storage/netlify-database/data-changes/>
- <https://www.netlify.com/knowledge-base/how-to-add-a-postgres-database-to-a-netlify-app/>

### Heroku (apps, git push remote, pipelines, promotion, review apps, rollback)

**Model.** The unit is an app: its own Heroku git remote, config vars, add-ons and numbered releases (slug + config). `git push heroku main` builds a slug and creates a release. 'Heroku only deploys code that you push to the master or main branches of the remote. Pushing code to another branch of the heroku remote has no effect.' Another local branch is deployed with a refspec, e.g. `git push prod-heroku production:master`. A pipeline is 'a group of Heroku apps that share the same codebase', in the stages Development, Review, Staging and Production. 'A Heroku app can belong to exactly one stage of exactly one pipeline.' A stage can hold several apps, e.g. a production app and an admin app running the same code with different config. Promotion copies the build artifact downstream: 'Heroku copies it without making any changes. It is not rebuilt for the environment of the target app.' Release phase does run on promotion. Promotion is recommended only for stateless builds. Review apps are 'complete, disposable' apps per GitHub PR, created automatically or manually and configured by app.json. They deploy the PR branch's HEAD, redeploy on each push, and (if auto-created) are destroyed when the PR closes.

**Terms.**

- App — deployable unit with its own git remote, config vars, add-ons and releases
- heroku remote — the per-app 'blessed' git remote; only main/master deploys
- Slug — the compiled build artifact
- Release — a slug plus a config version (v23, v54…)
- Pipeline — apps sharing one codebase across stages
- Stage — development, review, staging, production
- Promote — copy the upstream slug to the downstream app(s) without a rebuild
- Review app — ephemeral app per PR, built from app.json
- Review app config vars — pipeline-level (sensitive) values injected into every review app
- postdeploy / pr-predestroy — app.json scripts run once after review-app creation / before its destruction
- Release phase — command run on every deploy and promotion (e.g. migrations)
- Rollback — `heroku rollback vN` redeploys an earlier release

**Data.** Add-ons, such as Heroku Postgres, belong to each app. 'Pipelines manage the flow of application build artifacts only. Your app's Git repo, config vars, add-ons, and other environmental dependencies must be managed independently.' Review apps provision fresh add-ons from app.json; add-on providers may substitute an ephemeral default plan, and `environments.review` overrides the base config. Data is never copied: 'Copying full database contents to a Review apps is not currently supported. Copying production data to test apps means risk of data leaks… we recommend seeding databases comprehensively with non-production data using seed scripts run with the postdeploy command.' postdeploy runs once; release phase runs on every push. Rollback: 'The heroku rollback command does not roll back the state of… Add-on provisioning… Your app's Heroku-hosted Git repository… Any state stored in add-ons or externally' (add-on-related config vars do roll back). Historically (2013), `heroku fork` cloned the slug and config vars, re-provisioned add-ons and copied Postgres data.

**Bindings and secrets.** Config vars are per app, and promotion never touches them. A review app gets app.json env plus the pipeline's 'Review app config vars', which are injected at creation 'regardless of whether or not it is included in the app.json'. It also gets HEROKU_APP_NAME, HEROKU_BRANCH and HEROKU_PR_NUMBER. Some add-ons opt out of review/CI apps. Permissions on ephemeral apps are managed in the pipeline Access tab (auto-join with View/Operate/Deploy); changes apply only to new apps.

**Inbound (traffic, webhooks, cron).** Each app has its own hostnames and domains. Review app URLs are random by default; the predictable pattern 'can expose those apps to a possible subdomain takeover' and is not planned for Fir-generation apps. Disabling review apps deletes the existing ones.

**UX.** The pipeline overview page shows stages as columns and whether 'your production app is running different code than staging'. Each stage has a 'Promote' button, and there is a Review Apps column. Pipeline apps appear as one entry in the dashboard with a drop-down per app.

**Lessons.** (1) The per-app git remote is the canonical 'blessed runtime-injected remote': a push is a deploy, and exactly one branch counts. (2) Promotion moves artifacts, never config or data, so builds must be stateless for promotion to be safe. (3) Review apps get fresh data by policy and are seeded by script. (4) Rollback restores code and config but not data or add-ons.

**Sources.**

- <https://devcenter.heroku.com/articles/pipelines>
- <https://devcenter.heroku.com/articles/github-integration-review-apps>
- <https://devcenter.heroku.com/articles/git>
- <https://devcenter.heroku.com/articles/releases>
- <https://www.heroku.com/blog/heroku-pipelines-beta/>
- <https://github.com/heroku/heroku-pipelines>
- <https://stackoverflow.com/questions/59253626/how-to-make-rollback-commit-on-heroku>
- <https://www.exchangetuts.com/how-do-i-push-different-branches-to-different-heroku-apps-1639634827878366>

### Dokku and Piku (self-hosted git-push deploy)

**Model.** Dokku: `git remote add dokku dokku@host:app` then `git push dokku main`. The `deploy-branch` property (default master; `dokku git:set <app> deploy-branch X`, or global) decides which pushed branch triggers a deploy. 'As of 0.22.1, Dokku will also respect the first pushed branch as the primary branch, and automatically set the deploy-branch value at that time'. Deploying several branches needs a receive-branch plugin trigger. `git:sync <app> <repo> [ref]` pulls from a remote repo; it builds only with `--build`, and sets deploy-branch unless `--skip-deploy-branch`. Pushing to a nonexistent app auto-creates it unless `disable-autocreation` is set. `apps:clone <old> <new>` copies an app and rebuilds it (`--skip-deploy` to skip). `apps:lock` creates a persistent deploy lock under which pushes are rejected until `apps:unlock`. Review apps are a CI convention: dokku/gitlab-ci `review-apps:create` uses apps:clone, named `review-$APPNAME-$BRANCH_NAME`. Piku: `git remote add piku piku@server:appname`, then `git push piku master`, 'or if you want to push a different branch than the current one use `git push piku release-branch-name`'. Runtime detection and a Procfile of workers, with a `release` worker run once per deploy and a `preflight` worker run before it.

**Terms.**

- deploy-branch — Dokku property naming the one pushed branch that deploys
- git:sync — Dokku command to initialize or update an app from a remote repo
- apps:clone — copy an existing Dokku app into a new app and rebuild it
- apps:lock / apps:unlock — persistent deploy lock; pushes are rejected while locked
- review app (Dokku CI) — apps:clone-based per-branch app created by CI
- ENV file — Piku per-app configuration file committed in the repo (plus `piku config:set`)
- cron worker — Piku Procfile entry `cron: <simplified crontab> <cmd>`
- PIKU_AUTO_RESTART — Piku setting (default true); when false, deploy without restarting workers

**Data.** Apps are separate on the host. `apps:clone` caveats: custom domains are not applied, SSL certificates are not copied, and https:443 port mappings are skipped. Environment variables, 'including database connection strings', are preserved, so a clone points at the same database unless it is relinked (inference from the preserved env). There is no seeding concept.

**Bindings and secrets.** Configuration is per app (Dokku config; Piku ENV plus config:set). A clone inherits the source app's service URLs and credentials.

**Inbound (traffic, webhooks, cron).** Domains are per app, and clones don't get the custom domains. Piku cron workers are declared in the Procfile, so every app deployed from the same repo, including a copy, runs those crons (inference).

**UX.** CLI only: `apps:report` (including `--app-locked`, deploy source metadata), `git:report` (deploy-branch values; computed vs global).

**Lessons.** The self-hosted form of a 'blessed runtime remote': a branch filter decides what deploys, a lock is the hard pause, and PIKU_AUTO_RESTART=false is 'deploy but don't restart' (pause the reload, then restart by hand). Cloning an app copies config that points at shared services, which is the classic shared-DB pitfall.

**Sources.**

- <https://dokku.com/docs/deployment/methods/git/>
- <https://dokku.com/docs/deployment/application-management/>
- <https://dokku.com/docs/deployment/application-deployment/>
- <https://github.com/dokku/gitlab-ci/blob/main/README.md>
- <https://piku.github.io/>
- <https://github.com/piku/piku>
- <https://piku.github.io/configuration/procfile.html>
- <https://piku.github.io/configuration/env.html>

### Fly.io (apps, Machines, releases; review apps)

**Model.** The unit is an app whose Machines (VMs) run an image. `fly deploy` builds and rolls out a release with a strategy: `rolling` (the default), `immediate`, `canary` or `bluegreen` ('boot a new Machine alongside each running Machine… migrate traffic… only once all the new Machines pass health checks'). `fly releases --image` lists past releases and their images. Rollback is just `fly deploy --image <older>`: 'There's no special rollback command because you don't need one.' There are no built-in environments; an environment is a separate app (e.g. `-c fly.staging.toml`). Review apps come from the `superfly/fly-pr-review-apps` GitHub Action, which creates `pr-<number>-<owner>-<repo>` apps and destroys them when the PR closes. Machines created with `fly machine run` are unmanaged and not updated by `fly deploy`.

**Terms.**

- App — deployable unit; environments are separate apps
- Machine — a VM belonging to an app
- Release — a deployed image version, listed by `fly releases`
- Deployment strategy — rolling / immediate / canary / bluegreen
- Release command — one-off command in a temporary VM before the release (no volume access)
- Secrets — per-app encrypted values injected as env at Machine boot
- --stage — set secrets without restarting Machines
- Review app — per-PR app created by the GitHub Action

**Data.** Volumes are per Machine. For review apps, 'For production apps, it's a good idea to create a new Postgres cluster specifically for staging apps' (the action's `postgres` input attaches it); the Django guide: 'Using a dedicated staging database… eliminates a potential data leak vector.' Seeding is done with a release_command script (migrate + load fixtures). Rollback: 'you're rolling back the VM image, not the database… Fly isn't going to time-travel your data.'

**Bindings and secrets.** Secrets are per app. `fly secrets set` 'updates each Machine… This involves a restart', so 'fly secrets set triggers a new deployment'. With `--stage` the value is stored and applied only to Machines started or updated later, and `fly secrets deploy` redeploys the current release with the staged secrets. Rollbacks keep the current config and secrets: 'Rollbacks don't undo config changes.' Review-app secrets come from GitHub repository/environment secrets passed to the action. Warning: 'People with deploy access can deploy code that reads secret values.'

**Inbound (traffic, webhooks, cron).** Per-app hostnames `<app>.fly.dev`; review apps live at `https://pr-<n>-<owner>-<repo>.fly.dev`. Any cron-like Machines must be duplicated or omitted per app by hand (inference).

**UX.** Per-app dashboard and CLI; the GitHub Actions 'environment' shows the review app URL in the PR UI.

**Lessons.** 'Environment = separate app' is the simplest isolation (nothing is shared), but everything (secrets, databases, schedulers) must be duplicated by hand or by CI. A secret change is a deploy (with a staged option). Rolling back the image does not roll back config.

**Sources.**

- <https://fly.io/docs/launch/deploy/>
- <https://fly.io/docs/blueprints/rollback-guide/>
- <https://fly.io/docs/apps/secrets/>
- <https://fly.io/docs/flyctl/deploy/>
- <https://fly.io/docs/flyctl/secrets/>
- <https://fly.io/docs/blueprints/review-apps-guide/>
- <https://github.com/superfly/fly-pr-review-apps/>
- <https://fly.io/docs/django/advanced-guides/staging-environments-with-github-actions/>
- <https://community.fly.io/t/how-to-deploy-app-and-secrets-together/13647>

### Railway (project environments, PR environments, staged changes, sync)

**Model.** Project → environments → service instances. 'All projects in Railway are created with a production environment by default', and 'All changes made to a service are scoped to a single environment.' Persistent environments exist, e.g. staging auto-deploying from a `staging` branch; each service in each environment has its own trigger branch. PR environments are temporary: created when a PR opens and deleted on merge/close. They replicate the base environment (production by default, configurable). Focused PR Environments deploy only affected services plus their dependencies; Bot PR Environments is a toggle; PRs from users outside the workspace are not deployed. A new environment is either Duplicate (copy services, variables and config, with 'all services… staged for deployment. You must review and approve the staged changes') or Empty. Staged changes: config and variable changes wait for 'Deploy'. Sync imports services and config from another environment, tagged New/Edited/Removed, for review and deploy; 'Sync copies configuration, not data.' Forked environments were deprecated in January 2024 in favor of isolated environments plus Sync. Wait for CI holds a deploy in WAITING, and healthchecks gate the cutover.

**Terms.**

- Environment — isolated instance of every service in a project
- Persistent environment — long-lived, e.g. staging from the staging branch
- PR environment — ephemeral environment per PR, replicating a base environment
- Base environment — what PR environments replicate (production by default)
- Focused PR environments — deploy only services affected by the PR's changed files
- Duplicate / Empty environment — creation modes
- Staged changes — pending config changes that need 'Deploy'
- Sync — import services/config from another environment (never data)
- Reference variables — `${{Postgres.DATABASE_URL}}`-style references resolved inside the environment
- Sealed variables — secrets never shown or retrievable, excluded from duplication, PR envs, sync and integrations
- RAILWAY_ENVIRONMENT_NAME — injected environment name
- Environment RBAC — restrict non-admins from a sensitive environment's resources

**Data.** 'Each environment is an isolated instance of every service, so a duplicated environment gets its own database services with their own volumes and data'. Buckets are per environment with their own S3 credentials. Sync never copies data. Whether new database services start empty is not stated explicitly (inference: empty, since data isn't copied). The PR-canary guide warns: 'By default the canary copies production's variables, which may include a production DATABASE_URL. Use per-environment variables, or provision a per-PR database branch from your CI pipeline.'

**Bindings and secrets.** The private network is per environment: `<svc>.railway.internal` 'resolves only within the same environment. So a misconfigured staging worker that tries to call api.railway.internal gets the staging API, never the production one'. Reference variables resolve within the environment, so code and references stay identical everywhere. Every variable, including shared variables, is scoped to one environment. Sealed variables 'are excluded from environment duplication, PR environments, environment sync diffs, and external integrations'; duplicating 'is what you want: staging should hold staging credentials'. 'Your application should never branch on environment to pick credentials.' Under Environment RBAC, non-admins 'can see these environments exist but cannot access their resources… They can still trigger deployments via git push.'

**Inbound (traffic, webhooks, cron).** Domains are per environment. PR-environment services get Railway-provided domains only when their base-environment counterparts use them. A cron schedule is a per-service setting; the docs describe no cron suppression for PR/staging environments (inference: duplicated services keep their schedule, so crons can run in every environment).

**UX.** Environment dropdown in the top navigation; a canvas per environment; a staged-changes banner with Details and Deploy; a GitHub bot PR comment listing skipped services; PR environment toggles under Project Settings → Environments.

**Lessons.** The strongest example of a parallel binding fabric: identical service names, DNS and reference variables resolve inside the environment, so dev talks to dev with no code changes. Secret hygiene: sealed secrets never enter non-prod. The residual pitfall is literal (non-reference) variables copied from production, such as a hard-coded DATABASE_URL. 'Staged changes' is a pause-and-apply model for config.

**Sources.**

- <https://docs.railway.com/environments>
- <https://docs.railway.com/guides/isolate-staging-production>
- <https://docs.railway.com/guides/preview-deployments-with-pr-environments>
- <https://docs.railway.com/guides/ship-on-merge-pr-canaries>
- <https://docs.railway.com/integrations/api/manage-environments>
- <https://docs.railway.com/cron-jobs>

### Render (preview environments, service previews, projects/environments)

**Model.** Render has two preview mechanisms plus project environments. (1) Preview Environments are Blueprint-based (`previews.generation: off|manual|automatic`, `expireAfterDays`). Each PR 'creates new instances of the services and datastores defined in your Blueprint. These instances do not copy any data'; Preview Environment Initialization seeds them. (2) Service Previews (formerly PR previews; per-service `previews.generation`) 'only replicate the service with proposed changes'. 'Preview instances copy all of their settings over from their base service when they're first created. This includes environment variables, such as database connection information… Make sure to change environment variables on your preview instance if you want it to use a staging or test database.' Later base-service changes are not applied to the preview, and `IS_PULL_REQUEST` tells the app it is a preview. (3) Projects contain environments (production, staging). Env groups can be scoped to one environment. An environment can be 'protected' (only admins may perform destructive actions), and 'Block cross-environment connections' stops private-network traffic crossing its boundary. `fromDatabase`/`fromService` resolve within the same environment.

**Terms.**

- Blueprint (render.yaml) — infrastructure-as-code file
- Preview environment — full per-PR copy of Blueprint services and datastores, with empty data
- Service preview (PR preview) — single-service per-PR copy that inherits the base service's settings
- previews.generation — off / manual / automatic
- previewValue — env var override used only in preview environments
- previewPlan / previewDiskSizeGB — smaller preview instances
- sync: false — placeholder secret entered in the dashboard; not copied into previews
- Environment group — shared env vars/secret files, can be scoped to one project environment
- Protected environment — only admins may perform destructive actions
- Block cross-environment connections — private-network isolation of an environment
- IS_PULL_REQUEST — env var marking a service preview

**Data.** Preview environments get fresh, empty datastores, seeded by an initialization hook. A service preview shares whatever database the base service's env vars name, which is production unless edited. `previewValue` can deliberately point all previews at one shared database.

**Bindings and secrets.** `previewValue` overrides values for previews, e.g. a test API key. Placeholder `sync: false` secrets 'are not copied to preview environments' (the app may boot without them). The documented fix is a dashboard env group attached with `fromGroup`, which is copied: 'anything sensitive enough for sync: false is sensitive enough for an env group'. Env groups scoped to an environment can't be linked outside it. Within a project environment, `fromDatabase` 'resolves within the same environment'; you 'can't reference a database in one environment from a service in another'.

**Inbound (traffic, webhooks, cron).** Previews can be opted out per service; the tutorial: 'A worker that posts to your real billing webhook? Skip it. A cron that emails customers on a schedule? Definitely skip it.' (`previews.generation: off` on that worker or cron). Autoscaling is disabled in previews. Network isolation affects only private traffic, so public onrender.com URLs stay reachable.

**UX.** Render Dashboard: 'Create preview' for manual mode; the project page lists environments, each with a ••• menu holding 'Block cross-environment connections'; a protected-environment designation.

**Lessons.** The two tiers expose the core trade-off: a full-stack isolated preview (safe, empty data) vs a single-service preview that inherits production env (fast, dangerous). Workers and crons that have external side effects need an explicit per-service opt-out in previews. Secrets default to not copied.

**Sources.**

- <https://render.com/docs/preview-environments>
- <https://render.com/docs/service-previews>
- <https://render.com/docs/blueprint-spec>
- <https://render.com/tutorials/advanced-blueprint-patterns/preview-environments>
- <https://render.com/docs/projects>
- <https://render.com/tutorials/advanced-blueprint-patterns/projects-and-environments>
- <https://render.com/docs/configure-environment-variables>
- <https://render.com/docs/private-network.md>
- <https://render.com/changelog/block-private-network-across-environment-boundaries>

### Replit (live workspace vs published app; separate production database)

**Model.** The workspace (Project Editor) is a live dev environment with its own dev URL (`REPLIT_DEV_DOMAIN`). Publishing (formerly Deployments) serves a snapshot of the app at the public URL. Later workspace edits don't reach it until Republish, and 'The file system in published apps is not persistent and resets every time you publish'. The published app sees `REPLIT_DEPLOYMENT=1`.

**Terms.**

- Project Editor / workspace — live development environment
- Publish / Republish — create or refresh the published snapshot
- Development database — created with every app; Agent may change it freely
- Production database — created at publish; Agent can't touch it
- Deployment secrets — secrets as seen by the published app
- App Storage (formerly Object Storage) — account-level buckets
- REPLIT_DEPLOYMENT / REPLIT_DEV_DOMAIN — published vs dev markers

**Data.** 'Every Replit App works with two databases.' The development database is created automatically. The production database is 'created when you publish your app', and 'Agent can't touch it'. At publish, 'any changes you've made with Agent to the structure of your development database (adding and deleting columns or tables) are applied to your production database'. The engineering post describes the mechanism: a drizzle-kit-based schema diff generated at deploy time, applied first to a Neon branch of production and exercised by a temporary preview deployment, with the user asked to confirm, including destructive changes. First publish: 'production database with a fresh schema but no data' (2025-07 blog). On the legacy Neon path, 'Set up your production database with your current development data' copies it, and copying 'overwrites any existing production data'. Before 2025, development and production shared one database, and old forks/remixes 'could keep using the original app's database connection'; they are being migrated. App Storage buckets are account-level and 'lets you seamlessly share data between your development and production environments or with other Replit Apps'.

**Bindings and secrets.** Publishing sets `DATABASE_URL` to the production database automatically. The secrets docs contradict each other. The Publishing overview says 'Secrets sync automatically from your development environment to your published app'. The troubleshooting page says 'Secrets you set in the Project Editor do not automatically carry over to your published app. Add all production Secrets and environment variables in the Publishing pane.' The 2025 launch post calls DB separation 'the first step in establishing a unified development/production separation experience across Replit's cloud services (Secrets, Auth, Object Storage eventually)'.

**Inbound (traffic, webhooks, cron).** The published URL and the dev URL are distinct. The troubleshooting checklist includes updating login callback URLs, payment webhooks and CORS settings for production.

**UX.** Publishing pane: Publish/Republish and Production database settings. The Database panel shows both the development and production databases.

**Lessons.** The closest product to xbin's 'live workspace + protected live app with its own data'. Replit moved from a shared database to separate ones because dev edits could damage production. It promotes schema at publish with a deterministic diff, rehearsed on a branch of production. Separating secrets and storage lagged behind, and the docs still disagree, which shows how hard retrofitting is.

**Sources.**

- <https://docs.replit.com/features/data-and-storage/development-and-production>
- <https://docs.repl.it/references/data-and-storage/production-databases>
- <https://docs.repl.it/references/data-and-storage/create-production-database-when-publishing>
- <https://docs.replit.com/features/data-and-storage/shared-database-migration>
- <https://replit.com/blog/introducing-a-safer-way-to-vibe-code-with-replit-databases>
- <https://replit.com/blog/production-databases-automated-migrations>
- <https://docs.replit.com/features/publishing/overview>
- <https://docs.replit.com/core-concepts/project-editor/app-setup/secrets>
- <https://replit.mintlify.app/build/troubleshooting>
- <https://docs.replit.com/features/data-and-storage/object-storage>

### Val Town (always-live vals with branches)

**Model.** A val is 'a collaborative, versioned folder of deployed code' with HTTP, Cron and Email triggers, SQLite and blob storage, and environment variables. 'Every file change is automatically versioned. Rollbacks are instantaneous.' Every val has a `main` branch (the default; it cannot be renamed). Owners create branches off main or any other branch. A branch merges into its parent only if there are no conflicts, and can pull from its parent. Parents are fixed; there is no squash or rebase. Non-owners remix and send a pull request. 'Code on Val Town is always live, even on feature branches' ('Always-live branch previews'). From the Projects launch post (2025-01): 'just because you're deploying to the cloud on every save doesn't mean you want to deploy to production on every save. Val Town Project branches give you the best of both worlds.' Val Town built its own VCS rather than hosting git ('becoming a git host at scale was too much complexity').

**Terms.**

- val — versioned, deployed folder of code (formerly a project)
- main — default production branch
- branch — always-live copy with its own endpoints; merges back into its parent
- remix — copy of someone else's val, for pull requests
- trigger — HTTP / Cron / Email entry point
- val-scoped SQLite — `std/sqlite/main.ts`, one database per val
- organization-scoped SQLite — `std/sqlite/global.ts`, shared across an account's vals
- environment group — org-level bundle of env vars attached to vals

**Data.** Since 2026-01 each val gets its own SQLite database, 'accessible only by the val itself'. 'When you fork one of your own projects, you can choose to fork the database too' (seeded from Turso point-in-time backups). Val-scoped and user-scoped databases are both supported 'for the long term… there's no reason to force everyone into a gnarly upgrade path'. The docs do not say whether a branch gets its own database (inference: branches of a val share the val-scoped DB).

**Bindings and secrets.** Env vars are set per val, and org environment groups can be attached (val values take precedence). Branch-level scoping of env vars is undocumented (inference: shared with the val).

**Inbound (traffic, webhooks, cron).** Each branch serves live HTTP endpoints. The docs reviewed do not specify whether cron and email triggers fire on non-main branches (unknown).

**UX.** The val's navigation sidebar is where you create, delete, rename and merge branches; branch previews have their own URLs.

**Lessons.** A precedent for a 'save = live' product adding branches without abandoning live editing, and for additive storage migration (the old account-scoped DB is kept alongside new val-scoped DBs). Undocumented data and trigger semantics on branches are exactly the gap xbin's design must close explicitly.

**Sources.**

- <https://docs.val.town/vals/branches>
- <https://docs.val.town/vals>
- <https://www.val.town/features/version-control>
- <https://blog.val.town/projects>
- <https://blog.val.town/scoped-databases>
- <https://docs.val.town/reference/std/sqlite/usage/>
- <https://github.com/val-town/plugins/blob/main/plugin/skills/sqlite-storage/SKILL.md>
- <https://docs.val.town/vals/cron/>
- <https://docs.val.town/vals/http>

### Deno Deploy (timelines, active revision, timeline locking, per-timeline databases; Classic)

**Model.** App → revisions (builds; usually one per git commit) → timelines. The Production timeline 'contains all of the revisions from the default git branch' and owns the app URL and custom domains (production context). Each git branch has a 'Git Branch / <name>' timeline at `<branch>--<app>.<org>.deno.net` (development context). There is also one hidden preview timeline per revision, backing its preview URL. 'One of the revisions (usually the most recent one) is the active revision.' Rollback locks an older revision as active. Timeline locking: 'locking a timeline to a specific revision, to ensure that new builds do not automatically become the active revision… useful if you are in a feature freeze… you can still create new builds by pushing to Git, but they will not automatically become the active revision'. Unlocking reverts to 'the latest revision being the active revision'. The per-timeline rollout is: create the database if needed → pre-deploy command (migrations) → warmup (preview only) → route. Deno Deploy Classic had production vs preview deployments with no runtime difference, and GitHub deployments used a separate preview KV database.

**Terms.**

- Revision / build — one built version of the app
- Timeline — history of one branch: production, git-branch/<name>, preview/<revision>
- Active revision — the revision serving a timeline (latest unless locked)
- Timeline locking — pin the active revision; new builds don't activate
- Context — env var set: Production, Development (branches + previews), Build
- DENO_TIMELINE — runtime env var: production, git-branch/<name>, preview/<id>
- Pre-deploy command — runs once per timeline before traffic (migrations/seed)
- Database assignment — link a DB instance; logical databases created per timeline

**Data.** Once a database instance is assigned, Deno Deploy 'automatically creates isolated (logical) databases inside of that instance for each deployment environment': `{app-id}-production`, `{app-id}--{branch-name}`, and a single shared `{app-id}-preview` ('Currently only one preview database is created per app… In future releases, each preview deployment will get its own'). Deno KV works the same way: 'Deno Deploy automatically creates a separate database for each timeline.' Code needs no timeline detection: `Deno.openKv()` or the injected `DATABASE_URL`/`PG*` resolve to the right DB. Local development against the hosted DB is 'shared across all developers using the same app'. Seeding is via the pre-deploy command. Classic: 'Promoting deployments to production is restricted to deployments that already use the production KV database'. A bug (denoland/deno#24579) made `deployctl --prod` deploys use the preview KV: 'all of a sudden my prod domain showed all preview data.'

**Bindings and secrets.** Env vars are assigned to contexts: Production (production timeline), Development (branch and preview timelines), and Build, which is build-time only and not visible at runtime (`deno deploy env update-contexts`). `DENO_DEPLOYMENT_ID` identifies the whole configuration set (build, context, env vars, cloud connections, database).

**Inbound (traffic, webhooks, cron).** The production URL and custom domains map only to the production timeline; branch URLs and preview URLs map to their own timelines. Cron behavior on branch timelines was not verified.

**UX.** The Timelines page shows each timeline's active revision, with lock/unlock per revision; a revision's build page lists its timelines and preview URL.

**Lessons.** The cleanest 'branch-follows' model: one timeline per git branch, whose active revision follows new builds and can be locked (the pause) or rolled back. Binding data per timeline at runtime (late binding) avoids Classic's promotion restriction and the preview-KV-in-prod bug that came from binding data per build.

**Sources.**

- <https://docs.deno.com/deploy/reference/timelines/>
- <https://docs.deno.com/deploy/reference/databases/>
- <https://docs.deno.com/deploy/reference/deno_kv/>
- <https://docs.deno.com/deploy/reference/env_vars_and_contexts.md>
- <https://docs.deno.com/deploy/reference/builds/>
- <https://docs.deno.com/runtime/reference/cli/deploy/>
- <https://docs.deno.com/deploy/classic/deployments/>
- <https://github.com/denoland/deno/issues/24579>

### Supabase branching (preview and persistent branches)

**Model.** A main project plus branches: 'Each branch is a separate environment with its own Supabase instance and API credentials.' Preview branches are ephemeral and 'automatically deleted when a PR is merged or closed'. Persistent branches are long-lived (staging/QA/dev) and never auto-paused or auto-deleted. Branches come from the GitHub integration ('Automatic branching' maps each git branch to a Supabase branch, optionally only when Supabase files change) or from the dashboard. Each push runs a deployment workflow: clone → … → Configure (config.toml) → Migrate (pending migrations and vault secrets) → Seed (must be enabled for persistent branches) → Deploy (changed Edge Functions and function secrets). A failed step skips its dependents. 'Deploy to production' on merge applies new migrations plus the Edge Functions and Storage buckets declared in config.toml; other config is ignored for persistent branches unless opted in via `[remotes]`. Branching requires the Pro plan.

**Terms.**

- Preview branch — ephemeral per-PR Supabase instance
- Persistent branch — long-lived branch (staging, QA, dev)
- config.toml — config-as-code synced to every ephemeral branch
- [remotes.<name>] — per-persistent-branch overrides keyed by project_id
- seed.sql — seed data applied once when a branch is created
- Include data — dashboard branching option to start with data
- Deploy to production — GitHub-integration option to deploy on merge

**Data.** 'Cloning your base project copies its Edge Functions and configuration, but not its data or storage objects. This is meant to protect your sensitive production data. Your branch starts with the tables your migrations create, and the only rows in them are the ones your seed files add.' 'The database is only seeded once… To rerun seeding, delete the preview branch and recreate it.' 'Data changes in your seed files are not merged to production.' Branches are isolated in schema and data, storage objects, Edge Functions and auth config. Dashboard branches can opt in with 'Include data'.

**Bindings and secrets.** 'Secrets set for one branch are not automatically available in other branches' (`supabase secrets set --project-ref <branch-ref>`). Branch config can carry encrypted values in `.env.preview` (dotenvx) that the branching executor decrypts. Each branch has its own URLs and keys under Settings > API. An earlier release note: 'Edge Function secrets must be manually added to branches'.

**Inbound (traffic, webhooks, cron).** Each branch has its own API URL. The docs describe a branching webhooks processor (e.g. an Edge Function notifying Slack on run completion).

**UX.** A branch dropdown in the dashboard switches between branches.

**Lessons.** 'Data-less by default… to better protect your sensitive production data': empty schema from migrations plus seed files. Secrets are never inherited across branches. Config is code, with explicit per-remote overrides.

**Sources.**

- <https://supabase.com/docs/guides/deployment/branching>
- <https://supabase.com/docs/guides/deployment/branching/github-integration>
- <https://supabase.com/docs/guides/deployment/branching/configuration>
- <https://supabase.com/docs/guides/deployment/branching/working-with-branches>
- <https://supabase.com/docs/guides/deployment>
- <https://supabase.com/blog/cli-v2-config-as-code>

### Neon branching (copy-on-write Postgres branches)

**Model.** A project has a root/default branch, named `production` when created in the Console and `main` via the API/CLI. 'A child branch is a copy-on-write clone of the parent branch.' Each branch has its own computes. Creation options: Current data; Past data (point in time within the history window); Schema only (a new root branch with no parent and no shared history, which can't be reset); Anonymized data (PostgreSQL Anonymizer static masking with masking rules). 'Reset from parent' overwrites a child with the parent's latest schema and data: 'This reset is a complete overwrite, not a refresh or a merge.' The connection string is unchanged, connections are interrupted, and a branch with children can't be reset. Instant restore rewinds a branch to a point in time and keeps a backup branch. Any branch can be designated the default. The Neon-managed Vercel integration creates a branch per preview deployment, and per-branch Object Storage, Functions and AI Gateway endpoints are advertised too.

**Terms.**

- Root branch — branch without a parent (the project's first branch; schema-only branches too)
- Default branch — the branch new branches and integrations fork from (reassignable)
- Child branch — copy-on-write clone of its parent
- Schema-only branch — structure only, no rows; an independent root
- Anonymized branch — copy with masked sensitive data
- Reset from parent — overwrite a child with the parent's latest state
- Instant restore / backup branch — point-in-time rewind that preserves the old state
- Protected branch — can't be deleted or reset; rotates role passwords on children
- Branch expiration — auto-delete after a set time
- init_source — parent-data / parent-schema / schema-only / import

**Data.** Neon has the richest seeding toolbox: full copy-on-write clone (instant; shares storage until it diverges), point-in-time clone, schema-only, anonymized copy, and refresh via reset-from-parent. Schema-only exists 'for testing migrations or building new test data without exposing sensitive real-world data'.

**Bindings and secrets.** 'New passwords are automatically generated for Postgres roles on branches created from protected branches', so a child branch of production doesn't inherit production credentials. IP Allow can be restricted to protected branches. Each branch has its own connection string/compute.

**Inbound (traffic, webhooks, cron).** Not applicable (database).

**UX.** The Console bread-crumb branch picker. The CLI marks `[default]`, `[protected]` and `[current]`. Console-created schema-only branches default to auto-delete after 1 day.

**Lessons.** The model for how to seed dev data: copy-on-write clone, point-in-time clone, schema-only or anonymized, plus one-click refresh. Credential rotation on children of protected branches is a neat secret-hygiene default.

**Sources.**

- <https://neon.com/docs/manage/branches>
- <https://neon.com/docs/guides/branching-intro>
- <https://neon.com/docs/guides/reset-from-parent>
- <https://neon.com/docs/guides/branching-schema-only>
- <https://neon.com/docs/reference/api/branches>
- <https://neon.com/docs/cli/branches>

### PlanetScale branching (development/production branches, deploy requests)

**Model.** A database starts with a production branch `main`. 'Development branches… Please note that only the schema is copied. A new development branch will not have any data stored in it unless you restore from a backup.' Data Branching® seeds a branch from 'the latest backup of the Base branch' (schema plus data). Seeding from production creates a production-sized branch and bills accordingly, and 'PlanetScale does not provide data syncing between a production branch and a development branch.' With safe migrations enabled, a branch refuses direct DDL ('ERROR 1105 (HY000): direct DDL is disabled'). Schema changes go through deploy requests: a schema diff for review, optional required admin approval, a deploy queue with conflict analysis, and gated deployments. Unchecking 'Auto-apply changes' lets the migration finish and then waits for a manual 'Apply changes' cutover. A deploy can be reverted within 30 minutes while keeping data written in the meantime. Any development branch with a valid schema can be promoted to production. The staging pattern is a development branch with safe migrations used as the base for feature branches.

**Terms.**

- Production branch — highly available branch serving production traffic
- Development branch — schema-only isolated copy for experiments/CI
- Safe migrations — blocks direct DDL; changes only via deploy requests
- Deploy request — reviewed schema diff to merge into a base branch
- Deploy queue — ordered, conflict-checked application of deploy requests
- Gated deployment — prepared migration awaiting a manual 'Apply changes' cutover
- Revert — undo a schema deploy within 30 minutes, retaining new data
- Data Branching® — branch seeded from the latest backup
- Promote to production — turn a development branch into a production branch

**Data.** Schema-only by default; data only by restoring a backup; no ongoing sync; schema flows back only through deploy requests. Migration-tracking tables can be copied automatically on deploy.

**Bindings and secrets.** Each branch is a separate database endpoint with its own credentials (inference: standard per-branch connection strings).

**Inbound (traffic, webhooks, cron).** Not applicable (database).

**UX.** The branch overview has a 'Deploy to' dropdown; the deploy request page has 'Schema changes' diffs, 'Add changes to the deploy queue', 'Apply changes' and 'Revert changes'.

**Lessons.** Separates promoting code/schema (a reviewed diff, a queue, a gated cutover, a revert window) from data, which is never merged. A gated deployment is 'prepared but not yet live', i.e. 'reload now' for schema.

**Sources.**

- <https://planetscale.com/docs/vitess/schema-changes/branching>
- <https://planetscale.com/docs/vitess/schema-changes/data-branching>
- <https://planetscale.com/docs/vitess/schema-changes/safe-migrations>
- <https://planetscale.com/docs/vitess/schema-changes/deploy-requests>
- <https://planetscale.com/docs/vitess/schema-changes>
- <https://planetscale.com/docs/vitess/best-practices>

### Retool (live editing, releases, resource environments)

**Model.** Classic apps: 'Any changes you make to a Retool app are automatically saved to the current working version. This version is live to all users by default and immediately reflects your changes. This version isn't numbered and is referred to as latest.' 'After you publish the first release for an app, you can make changes to the current working version without affecting users.' Releases are numbered (Major/Minor/Patch). You can create draft releases, 'Create and publish', or publish any release, which then shows the 'Live' label. 'Unpublish release… does not publish a previous release and restores the current working version, latest, as the live version for all users.' The History tab reverts the working version to a history point without changing the published release. URL params: `_releaseVersion`, `_historyOffset`. Viewing unpublished versions needs the 'Allow access to unpublished versions' permission. Source-controlled (protected) apps: edits happen on branches/threads, and merging to main creates a published version. You can tag releases and choose 'Which tag should be pinned for publishing?': Latest changes / Current tag / This tag, later 'Always publish latest changes' or a chosen tag. Resource environments are orthogonal to versions: 'apps function across resource environments. When you query a resource in an app, the resource's environments determine the data the app uses.'

**Terms.**

- Current working version (latest) — autosaved edits; live to users until the first release
- Release — numbered snapshot; draft or published ('Live')
- Publish / Unpublish release — make a release live / return to 'latest' being live
- History / Revert — per-change history of the working version
- Resource environment — named set of per-resource configurations (production is default and can't be removed/renamed; staging; custom)
- Resource configuration — environment-specific credentials/hosts of one resource
- Configuration variables — environment-specific values or secrets
- Protected app — source-controlled app edited on branches
- Pinned for publishing — whether users get the latest published changes or a chosen tag
- _releaseVersion / _environment / _historyOffset — URL parameters
- retoolContext.pageTag / environment — runtime version tag ('latest' for editors) and environment name

**Data.** Data isolation comes from resource configurations: each resource (database, API) can hold a different connection per environment, and the environment the user or URL selects decides which is used. Retool Database is 'automatically configured for any additional environments… Each environment contains an isolated set of tables'. Schema changes move between environments by schema migrations; data never moves. Enterprise guidance: separate Retool instances or Spaces per environment.

**Bindings and secrets.** A resource must be configured for an environment before an app using it can switch there; 'you cannot switch to a resource environment that is not configured with a required resource'. Users re-authenticate per environment, and 'Retool then stores separate access tokens for each resource environment.' Configuration variables (values or secrets) are per environment, with an optional default. Business/Enterprise plans get per-environment resource permissions (e.g. staging data yes, production no).

**Inbound (traffic, webhooks, cron).** Public Apps always use production. Editors and admins can switch environments in the IDE or preview. The selection 'is saved to your browser cache and automatically [used] the next time you open an app', and `_environment` links open an app in a given environment.

**UX.** Releases and history icon in the left panel; 'Live' label on the published release; the app version shown in the status bar; environment switcher in the IDE; `retoolContext.pageTag` is 'latest' for editors and the version tag for viewers. A community thread shows users confused when edits appeared live before any release.

**Lessons.** The exact precedent for xbin's compatibility constraint. Live editing is the default and stays valid; releases are opt-in, and the first publish flips the semantics; Unpublish returns to live mode, so nothing becomes legacy. Environment (which data/credentials) is a separate axis from version (which code), and the data split comes from per-environment resource configurations.

**Sources.**

- <https://docs.retool.com/apps/guides/app-management/releases-history>
- <https://docs.retool.com/mobile/guides/deploy/releases-history>
- <https://docs.retool.com/build/apps/guides/source-control/edit-publish>
- <https://community.retool.com/t/retool-apps-showing-in-progress-changes-before-release/59662>
- <https://docs.retool.com/org-users/guides/configuration/environments>
- <https://docs.retool.com/org-users/guides/configuration/config-vars>
- <https://docs.retool.com/data-sources/guides/retool-database/multiple-environments>
- <https://docs.retool.com/education/coe/customer-resources/environments>
- <https://registry.terraform.io/providers/tryretool/retool/latest/docs/resources/resource_configuration>

### Firebase Hosting preview channels (plus App Hosting environments)

**Model.** Site → channels. The 'live' channel serves `SITE_ID.web.app`, `SITE_ID.firebaseapp.com` and connected custom domains; it never expires and keeps a release history for rollback. Preview channels serve 'temporary, sharable preview URLs' (`SITE_ID--CHANNEL_ID-RANDOM_HASH.web.app`); they expire after 7 days by default (up to 30 via `--expires`), and 'rolling back is not yet available' for them. Every deploy creates a release pointing to a version (content + Hosting config). The commands are `firebase hosting:channel:deploy CHANNEL_ID` and, to promote, `firebase hosting:clone SOURCE_SITE_ID:SOURCE_CHANNEL_ID TARGET_SITE_ID:live`. Clone creates a new release pointing at the exact same version, and works across sites and even projects. The GitHub Action creates a channel per PR and can deploy live on merge. App Hosting supports environments as separate Firebase projects, each backend tagged with an 'Environment name' that selects an `apphosting.<ENV>.yaml` override.

**Terms.**

- Site — a Hosting site within a project
- Channel — live or preview; each serves its own content and config at a URL
- Version — packaged content and Hosting config
- Release — a deployment record pointing to a version
- Clone — copy a version to another channel/site/project (promotion)
- Expiration — preview channel lifetime (default 7 days, max 30)
- Pinned functions — functions pinned in rewrites, deployed with a channel
- Environment name (App Hosting) — maps a backend to apphosting.<ENV>.yaml

**Data.** 'When using a preview URL, your web app interacts with your real backend for all project resources (with the exception of any pinned functions in your rewrites config)'. The GitHub integration doc repeats: 'Reminder: When using preview URLs, your app interacts with the real backend resources of your Firebase project.' Google's environment guidance is separate projects: 'Builds based on release status should not share the same Firebase resources because that risks your debug data polluting or even overriding your prod data'. Tag the production project with the 'production' environment type, and use the Local Emulator Suite for single-user environments.

**Bindings and secrets.** Channels version only static content and Hosting config (plus pinned functions). Firestore, Functions, Auth and every other project resource are shared with live. Environment separation means a separate project, with separate secrets (App Hosting references Cloud Secret Manager).

**Inbound (traffic, webhooks, cron).** Custom domains serve only the live channel; preview URLs are random-hash subdomains. The security guidance suggests preview URLs as a way to limit access to pre-prod.

**UX.** The Hosting dashboard shows the live channel's release history table and lists preview channels; a per-channel view allows revert and delete. The live channel can't be deleted.

**Lessons.** Frontend-only previews over a shared production backend are cheap but mean 'preview = production data'. Real isolation is 'project per environment', i.e. a completely separate stack.

**Sources.**

- <https://firebase.google.com/docs/hosting/manage-hosting-resources>
- <https://firebase.google.com/docs/hosting/test-preview-deploy>
- <https://firebase.google.com/docs/hosting/github-integration>
- <https://firebase.google.com/docs/projects/dev-workflows/general-best-practices>
- <https://firebase.google.com/docs/projects/dev-workflows/overview-environments>
- <https://firebase.google.com/docs/projects/dev-workflows/general-security-guidelines>
- <https://firebase.google.com/docs/app-hosting/multiple-environments>

### Azure App Service deployment slots (plus Azure Functions slots)

**Model.** An app has a production slot plus deployment slots (Standard/Premium/Isolated tiers; e.g. Standard allows 5; 'no extra charge'). Slots 'are live apps with their own host names' (`sitename-slotname.azurewebsites.net`). 'The new deployment slot has no content, even if you clone the settings from a different slot', and a slot can be deployed from another branch or repo. A swap proceeds in steps. (1) Apply the target slot's slot-specific settings (plus continuous deployment and auth settings) to all source instances, which restarts them. (2) Wait for the restarts; if any fails, revert. (3) Warm up via local cache, `applicationInitialization` or a request to root. (4) Switch the routing rules. (5) Apply the settings to the swapped-out old app. 'The target slot remains online while the source slot is prepared and warmed up.' 'Swap with preview' pauses after phase 1 so you can validate the source running with production settings, then Complete or Cancel. Rollback is swapping again. Auto swap swaps on every push to a slot (not on Linux or containers).

**Terms.**

- Production slot — the slot behind the app's production URL
- Deployment slot — live non-production copy of the app with its own hostname
- Swap — exchange content and swappable config between two slots after warm-up
- Swap with preview (multi-phase swap) — pause after applying target settings, for validation
- Auto swap — automatically swap a slot into its target on push
- Slot setting / 'Deployment slot setting' (sticky) — setting that stays with its slot across swaps
- Traffic % — share of production traffic randomly routed to a slot
- x-ms-routing-name — cookie/query param that pins or selects a slot (self = production)

**Data.** Slots are copies of code and config; data lives outside and is reached through connection strings. Connection strings swap with the code unless marked sticky, so whether swapped-in code talks to the staging or production database depends on stickiness. Swapped: language/framework settings, 32/64-bit, WebSockets, app settings and connection strings (unless sticky), mounted storage (unless sticky), handler mappings, public certificates, WebJobs content, hybrid connections, service endpoints, CDN, path mappings. Not swapped: protocol settings, publishing endpoints, custom domains, non-public certs/TLS, scale settings, WebJobs schedulers, IP restrictions, Always On, diagnostics, CORS, managed identities, `_EXTENSION_VERSION` settings, Service Connector settings, VNet integration.

**Bindings and secrets.** Settings are individually marked sticky via the 'Deployment slot setting' checkbox. Managed identities stay with their slot, so identity does not travel with code. Functions docs: 'Settings related to event sources and bindings must be configured as deployment slot settings before you start a swap'; 'When you create a new staging slot, all existing settings from the production slot are created in the new slot, regardless of the stickiness of the setting.' Swapping resets keys when `AzureWebJobsSecretStorageType` is files.

**Inbound (traffic, webhooks, cron).** Custom domains and WebJobs schedulers stay with the slot. Traffic routing: set a slot's Traffic %, and randomly routed clients are pinned for an hour by the `x-ms-routing-name` cookie; `?x-ms-routing-name=staging` opts in, and `self` opts back into production. An explicitly set 0% keeps a slot reachable only via the parameter. Background triggers run in every slot: 'It's common to disable timer triggered functions in slots to prevent simultaneous executions', using a sticky `AzureWebJobs.<name>.Disabled`. Known issue azure-functions-host#8999: during swap warm-up the staging slot runs with production settings, so a disabled timer re-enables and 'you can end up with a timer firing from both a prod slot and a stage slot'; WEBSITE_SLOT_NAME is not reliably updated during the swap. The SQL trigger extension saw duplicate lease tables and executions during swaps (azure-functions-sql-extension#882).

**UX.** The Deployment slots page has a Traffic % column. The Swap dialog has 'Source slot changes' and 'Target slot changes' tabs, i.e. a diff of the config changes a swap will apply. There is a per-setting 'Deployment slot setting' checkbox, and each slot appears as a separate 'App Service (Slot)' resource.

**Lessons.** The most detailed prior art on which settings belong to the deployment vs travel with the code: an explicit sticky/swappable list, plus a pre-swap diff UI. Pitfalls: background triggers run in non-production copies unless explicitly disabled; warm-up briefly runs staging code with production config; stickiness is per setting and easy to get wrong.

**Sources.**

- <https://learn.microsoft.com/en-us/azure/app-service/deploy-staging-slots>
- <https://learn.microsoft.com/en-us/azure/azure-functions/functions-deployment-slots>
- <https://learn.microsoft.com/en-us/azure/azure-functions/disable-function>
- <https://github.com/Azure/azure-functions-host/issues/8999>
- <https://github.com/Azure/azure-functions-sql-extension/issues/882>
- <https://learn.microsoft.com/en-us/answers/questions/1847493/disabled-functions-on-staging-environment-are-fire>
- <https://learn.microsoft.com/en-us/cli/azure/webapp/traffic-routing?view=azure-cli-latest>

### Convex (dev / preview / prod deployments with a file-watching dev loop) — extra

**Model.** 'By default, each project has a single shared prod deployment and each developer working on the project has their own dev deployment.' Preview deployments are created per branch and cleaned up after 5 days (14 on higher plans). You can add more deployments, e.g. `npx convex deployment create staging --type prod`, and local deployments. `npx convex dev` 'continuously pushes backend code you write in the convex/ folder to your deployment' on every file change, enforcing schema changes. `npx convex deploy` pushes to production, or to a preview deployment when CONVEX_DEPLOY_KEY is a preview key (named after the git branch in CI). `npx convex deployment select` changes which deployment `convex dev` targets. Deploy keys can be scoped to a single deployment, for CI or coding agents.

**Terms.**

- Deployment — named backend with its own code, data, functions and scheduled functions
- Deployment reference — dev/[creator], preview/[branch], production, staging
- npx convex dev — watch-and-push loop bound to one selected dev deployment
- npx convex deploy — explicit push to prod or a preview
- --preview-run — function run once when a preview deployment is created (seeding)
- Project environment variable defaults — per-type (dev/preview/prod) defaults copied into new deployments
- Deploy key — credential scoped to one deployment

**Data.** 'Each Convex deployment contains its own data, functions, scheduled functions, etc.' Production 'has separate data and has a separate push process from personal dev deployments'. Previews are seeded with `npx convex deploy --preview-run <function>`, which runs only when a preview is created (a same-named preview is reused with its data; `--preview-create` recreates it).

**Bindings and secrets.** Environment variables are per deployment. Project defaults per deployment type 'will be used when creating a new deployment, and will have no effect on existing deployments (they are not kept in sync)'; the dashboard flags drift from the defaults. 'Environment variables used in cron definitions will only be reevaluated on deployment.' Dashboard permissions: every team member may edit dev deployments; production only project/team admins.

**Inbound (traffic, webhooks, cron).** Each deployment has its own URL. Crons and scheduled functions live in each deployment (the agent-mode doc cites 'crons' as a reason to use a cloud dev deployment), so crons run in dev deployments too (inference).

**UX.** Dashboard deployment picker; CLI `--prod` / `--deployment <ref>`; `npx convex deployment select`.

**Lessons.** The closest match to 'hot reload attaches to at most one deployment': the watcher is bound to one selected dev deployment, and production changes only by an explicit deploy. Env defaults copied once at creation and never synced mirror Cloudflare's Previews Base.

**Sources.**

- <https://docs.convex.dev/understanding/workflow>
- <https://docs.convex.dev/cli/reference/dev>
- <https://docs.convex.dev/production/multiple-deployments>
- <https://docs.convex.dev/cli/reference/deployment>
- <https://docs.convex.dev/production/environment-variables.md>
- <https://docs.convex.dev/cli/reference/env.md>
- <https://docs.convex.dev/cli/reference/deploy.md>
- <https://docs.convex.dev/cli/agent-mode>
- <https://stack.convex.dev/seeding-data-for-preview-deployments>

### AWS Amplify Gen 2 (fullstack branches, PR previews, cloud sandboxes) — extra

**Model.** Fullstack branch deployments: each connected git branch (optionally matched by a pattern via branch auto-detection) gets its own frontend plus CloudFormation backend. 'All shared environments (such as production, staging, gamma) map 1:1 to Git branches', and promotion is a git merge. Fullstack PR previews: 'ephemeral fullstack environments on every pull request' at `pr-1.appid.amplifyapp.com`, torn down on merge/close. Alternatively previews can reuse the `dev` branch's backend (`npx ampx generate outputs --branch dev`) 'so you can reuse seed data, users, and groups'. Per-developer cloud sandbox: `npx ampx sandbox` deploys stack `amplify-<app>-<user>-sandbox`, then 'watches for file changes in your amplify/ folder and performs real-time updates' (CDK hotswap). Multiple sandboxes are possible via `--identifier`, but 'only one can be running at a time'.

**Terms.**

- Fullstack branch — git branch with its own deployed frontend and backend
- Branch auto-detection — automatically connect branches matching a pattern
- Fullstack PR preview — ephemeral environment per pull request
- Cloud sandbox — per-developer backend that redeploys on save
- amplify_outputs.json — client config that names which backend to use
- secret() — backend secret reference, configured per app or branch

**Data.** Every branch, PR and sandbox gets separate backend resources by default. Sharing is an explicit build-setting choice, e.g. pointing previews or feature branches at the dev backend.

**Bindings and secrets.** The client binds to a backend through `amplify_outputs.json`, generated per environment. 'To deploy a backend that uses secret() references via Amplify hosting, the secret values must be configured for the Amplify app or branch.'

**Inbound (traffic, webhooks, cron).** Per-branch and per-PR URLs; production follows the main branch.

**UX.** Amplify console: Hosting > Previews has a 'Pull request previews' toggle; App settings > Branch settings configures auto-detection and auto-disconnection.

**Lessons.** An explicit knob for 'does a preview get its own backend or share dev's', i.e. the parallel-binding-fabric question answered per environment. A personal sandbox with a file watcher is the cloud analogue of a hot-reload-attached dev deployment.

**Sources.**

- <https://docs.amplify.aws/javascript/deploy-and-host/fullstack-branching/branch-deployments/>
- <https://docs.amplify.aws/javascript/deploy-and-host/fullstack-branching/pr-previews/>
- <https://docs.amplify.aws/javascript/deploy-and-host/fullstack-branching/share-resources/>
- <https://docs.amplify.aws/react/deploy-and-host/sandbox-environments/setup/>
- <https://docs.amplify.aws/javascript/deploy-and-host/sandbox-environments/features/>
- <https://docs.amplify.aws/react-native/how-amplify-works/concepts/>

### Shopify themes (live theme vs development themes with hot reload) — extra

**Model.** A store has one live (published) theme plus unpublished themes in its theme library. `shopify theme dev` uploads a hidden development theme and serves a local preview that 'can hot reload local changes to CSS and sections, or refresh the entire page when a file changes… using the store's data'. If you already have a development theme, it is replaced; `--live-reload hot-reload|full-page|off` picks the mode. Developing directly on the live theme requires `-a, --allow-live`. Development themes 'don't count toward your theme limit, and are deleted from the store after seven days of inactivity', or on `shopify auth logout`. `theme push --unpublished` shares a stable copy, and `theme publish` makes a theme live.

**Terms.**

- Live (published) theme — what customers see
- Unpublished theme — stored in the theme library, previewable
- Development theme — temporary hidden theme bound to a developer's `theme dev` session
- Publish — make a theme the live theme
- --allow-live — explicit opt-in to hot-reload or push onto the live theme
- Preview link — shareable link to a non-live theme

**Data.** Development themes read the real store data ('use that store's data for local testing'). Themes are presentation-only, so there is no data separation.

**Bindings and secrets.** Store apps, settings and data are shared by all themes (inference).

**Inbound (traffic, webhooks, cron).** Customers only ever get the live theme; everyone else uses preview links.

**UX.** Admin theme library (live vs unpublished); the CLI prints the local URL, theme-editor link and shareable preview link.

**Lessons.** Hot reload attaches to a dev copy by default, and pointing it at production requires a guard flag. A dev copy can safely share production data only because themes are presentation-only, which does not hold for xbin backends.

**Sources.**

- <https://shopify.dev/docs/storefronts/themes/tools/cli>
- <https://shopify.dev/docs/api/shopify-cli/theme/theme-dev>
- <https://shopify.dev/docs/storefronts/themes/getting-started/customize>
- <https://shopify.dev/docs/storefronts/themes/getting-started/create>

### n8n (2.0 save-vs-publish split; git-branch-backed environments) — extra, a cautionary tale

**Model.** n8n 1.x: an active workflow went live whenever it was saved (Activate/Deactivate toggle). n8n 2.0 (BREAKING): 'Save stores your changes without affecting production… Publish explicitly makes your workflow live.' Edits autosave every 1–5 s as draft versions; 'Production executions always point to the currently published version.' Publishing shows a diff of saved vs published first. The Publish button has states ('Published, up to date', 'Published, has changes'…). Version history supports unpublish, restore, publishing another version, and naming a version to protect it from pruning. On upgrade, 'Your existing active workflows will automatically be marked as published.' Parent workflows reference only published sub-workflows. The Public API `PUT /workflows/:id` still republishes by default: `publishIfActive` defaults to true 'so existing integrations keep their current behavior', and `?publishIfActive=false` saves a draft (n8n@2.35.0). Publishing is a separate permission from editing: an API key needs `workflow:activate` and the role `workflow:publish`, while `workflow:update` alone writes drafts (PR #37264). Bug #23549: in 2.0/2.1 the production webhook URL executed the latest saved draft, not the published version (fixed in 2.2.2/2.1.5). Environments come from source control: each instance links to a git branch. The recommended multi-instance, multi-branch setup is push from dev, merge a PR, pull into prod (the pull can be automated via `/source-control/pull`). A 'Protected instance' blocks editing, and an instance color label 'helps users know which instance they're in'.

**Terms.**

- Save (draft) — autosaved new workflow version, not live
- Publish / Unpublish — make a version live / stop the workflow
- Published version (activeVersionId) — the version production executions use
- publishIfActive — Public API flag preserving the old save-means-live behavior
- Protected instance — instance whose source-controlled resources can't be edited
- Push / Pull — move workflows between an instance and its git branch
- Credential/variable stubs — pushed placeholders; secret values stay per instance

**Data.** Executions data and credentials stay per instance. Git carries workflows, tags, and variable/credential stubs, never secret values. 'n8n pushes the current saved version, not the published version.'

**Bindings and secrets.** Credentials live per instance, with stubs in git; pulling assigns them to matching users/projects.

**Inbound (traffic, webhooks, cron).** Webhooks and schedules must run the published version. The 2.0 webhook path initially bypassed this and ran drafts.

**UX.** Publish button (Shift+P) in the canvas header with explicit states; a history icon; a publish modal with a diff; an instance color next to the push/pull buttons.

**Lessons.** A real migration from live-on-save to draft/publish. It was BREAKING, needed an auto-migration (active → published), and kept the API's old default for compatibility. One inbound path (webhooks) initially bypassed the pin, so every entry point must resolve through the same 'which version is live' pointer. Promotion became a distinct permission.

**Sources.**

- <https://support.n8n.io/article/understanding-workflow-publishing-in-n-8-n-2-0>
- <https://docs.n8n.io/build/understand-workflows/save-and-publish-workflows>
- <https://docs.n8n.io/changelog/v20-breaking-changes>
- <https://github.com/n8n-io/n8n/issues/35813>
- <https://github.com/n8n-io/n8n/pull/37264>
- <https://github.com/n8n-io/n8n/issues/23549>
- <https://docs.n8n.io/administer/use-source-control-and-environments/choose-branching-patterns>
- <https://docs.n8n.io/administer/use-source-control-and-environments/move-work-between-environments>
- <https://docs.n8n.io/administer/use-source-control-and-environments/push-and-pull-changes>
- <https://docs.n8n.io/administer/use-source-control-and-environments/set-up-source-control>

### Darklang ('deployless' live editing with feature flags) — extra, the opposite design point

**Model.** 'Deployless means that anything you type is instantly deployed and immediately usable in production.' Code that has traffic flowing through it is 'locked' and must be changed with structured tools. Feature flags cover HTTP/event handlers: 'Feature flags… create a new sandbox in production, which replaces the dev environment'. A flag's condition selects which traffic runs the new code (e.g. a query param); commit makes it run for all, and discard restores the old code. Functions and types are versioned, with each caller pinned and upgraded case by case. Databases change through versioned schemas (Users-v0 → Users-v1), switched per handler and then converted in the background. Trace-driven development replays real requests, and side-effecting expressions need an explicit play button.

**Terms.**

- Deployless — every edit is live immediately
- Feature flag — in-editor branch of a handler with a traffic condition; commit or discard
- Locked code — code with traffic, editable only through flags/versions
- Function/type versions — per-caller pinning with explicit upgrade
- Traces / live values — real requests replayed while editing

**Data.** There is a single set of production data. Schema evolution goes through versioned datastores. 'Instant database clones' were planned, but the team 'haven't found these to be as important so far'.

**Bindings and secrets.** Not applicable.

**Inbound (traffic, webhooks, cron).** Each request picks its code path through the flag condition; side effects during editing require explicit confirmation.

**UX.** Flags are inline in the editor; the command palette commits or discards; 'you can only have one feature flag per component'.

**Lessons.** Isolation at request granularity instead of per environment. It clashes with xbin's 'dev must not see prod data', but offers two ideas: gate side effects during live iteration, and treat code in use as locked.

**Sources.**

- <https://blog.darklang.com/how-dark-deploys-code-in-50ms/>
- <https://docs.darklang.com/how-to/feature-flags>
- <https://docs.darklang.com/reference/faqs>
- <https://docs.darklang.com/discussion/trace-driven-development>

### Glitch remix (fork semantics for live-edited apps) — extra

**Model.** Projects are edited live. 'Remix' copies a project into your account.

**Terms.**

- Remix — copy of a project
- `.env` — secrets file; values scrubbed on remix, names kept
- `.data` — persistent data directory, not copied on remix

**Data.** The contents of '.data' are not copied on remix, so a remix starts without the original's data (the hello-sqlite template relies on this).

**Bindings and secrets.** 'When remixing an app, the values that you enter in the .env are automatically cleared so they're not copied across', but the variable names stay as placeholders. A Remix URL can prefill values explicitly (`?var1=value1`).

**Inbound (traffic, webhooks, cron).** A remix gets its own project URL.

**UX.** Remix button; the .env editor.

**Lessons.** A clean default for copies: code yes; data no; secret values no; secret names yes, as placeholders to fill in.

**Sources.**

- <https://fastly.my.site.com/GlitchHelpCenter/s/article/Remix>
- <https://fastly.my.site.com/GlitchHelpCenter/s/article/Adding-Private-Data>
- <https://fastly.my.site.com/GlitchHelpCenter/s/article/How-do-I-configure-a-Remix-so-my-users-can-get-started-in-one-click>
- <https://dev.to/glitch/hello-to-the-new-hello-sqlite-5044>
