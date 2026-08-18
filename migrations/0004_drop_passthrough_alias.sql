-- Remove the catch-all "*" route.
--
-- It forwarded any unrecognised model name to a provider, which existed so a
-- simple install needed no model configuration at all. Picking models is now
-- two clicks, and the catch-all's real cost became the deciding one: a typo in
-- a model name was forwarded upstream and came back as a vendor's 404 instead
-- of Polyglot saying plainly that it does not know that model.
--
-- Any row left here would appear in the aliases list as an alias literally
-- named "*" that no longer resolves, so they go.

DELETE FROM model_aliases WHERE alias = '*';
