-- One provider, several wire protocols.
--
-- Some services expose the same account on more than one API and decide per
-- model which one is native: Claude on /messages, GPT on /responses, open
-- models on /chat/completions. Until now a provider carried exactly one
-- protocol, so such a service needed one provider row per protocol and the
-- operator had to know which model lived on which.
--
-- models.protocol is a per-model override. Empty means "use the provider's
-- protocol", which is every row that exists today, so nothing changes for a
-- single-protocol provider.
--
-- providers.vendor names a built-in multi-protocol upstream ('opencode-go',
-- 'commandcode'). Polyglot then knows each protocol's base URL and how to
-- authenticate, and base_url is only the address shown to the operator. Empty
-- means an ordinary provider that is reached at base_url with its protocol.
ALTER TABLE models ADD COLUMN protocol TEXT NOT NULL DEFAULT '';
ALTER TABLE providers ADD COLUMN vendor TEXT NOT NULL DEFAULT '';
