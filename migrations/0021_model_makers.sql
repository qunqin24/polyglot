-- Who made each model, from models.dev's canonical_model_id: "glm-5.2" is
-- zhipuai's even where Alibaba resells it. Keyed by the same normalised id as
-- price_catalog and replaced with it in one transaction, but covering every
-- provider models.dev lists rather than only the first-party ones prices come
-- from. Nothing prices from it; the WebUI uses it to pick a model's logo.
CREATE TABLE model_makers (
    model_id TEXT PRIMARY KEY,
    maker    TEXT NOT NULL
);
