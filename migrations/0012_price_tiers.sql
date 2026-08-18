-- Long-context pricing.
--
-- Several vendors charge more once a prompt passes a length: OpenAI above
-- 272k tokens, Google above 200k, xAI above 128k. It is the whole schedule
-- that changes, not just the input price, so a tier carries its own four
-- numbers beside the base ones.
--
-- 26 first-party models have one today, and they are the expensive ones —
-- gpt-5.6, gemini-2.5-pro, grok-4.6. A long prompt is also the costly kind of
-- request, so pricing every one of them at the base rate got the largest
-- amounts most wrong.
--
-- Only the catalog gets tiers. An operator's override stays four flat numbers:
-- they stated one price, and charging them a multiple they never mentioned
-- would be putting a number in their mouth.

-- NULL means this model has no long-context rate, which is the common case.
ALTER TABLE price_catalog ADD COLUMN tier_above_tokens INTEGER;
-- Only what the vendor restates above the threshold; the rest keeps the base
-- price, so a tier that mentions no cache price is not read as a free one.
ALTER TABLE price_catalog ADD COLUMN tier_input REAL;
ALTER TABLE price_catalog ADD COLUMN tier_output REAL;
ALTER TABLE price_catalog ADD COLUMN tier_cache_read REAL;
ALTER TABLE price_catalog ADD COLUMN tier_cache_write REAL;
