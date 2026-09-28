# Golden expectations

Implementation slices should add normalized `Document` JSON snapshots here.
Snapshots cover metadata, ordered block kinds/text and source offsets, skip and
semantic flags, outline title/role/ranges/confidence, and warnings. They omit
absolute source paths and generated IDs so fixtures stay portable. Do not
snapshot dependency-specific AST details.
