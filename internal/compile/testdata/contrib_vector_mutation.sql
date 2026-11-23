-- field: createEmbedding
WITH __mut AS (
  INSERT INTO "public"."embeddings" ("embedding", "half", "sparse")
  VALUES ($1::"ext".vector, $2::"ext".halfvec, $3::"ext".sparsevec)
  RETURNING "id", "embedding", "half", "sparse"
)
SELECT json_build_object('embedding', json_build_object('id', __t_1."id", 'embedding', (__t_1."embedding")::text::json, 'half', (__t_1."half")::text::json, 'sparse', __t_1."sparse")) AS data
FROM __mut AS __t_1
-- args: ["[1,2.5,-3]","[0.5,1,2]","{1:1,3:2}/3"]

-- field: updateEmbeddingById
WITH __mut AS (
  UPDATE "public"."embeddings"
  SET "embedding" = $1::"ext".vector, "sparse" = $2
  WHERE "public"."embeddings"."id" = $3
  RETURNING "embedding"
)
SELECT json_build_object('embedding', json_build_object('embedding', (__t_1."embedding")::text::json)) AS data
FROM __mut AS __t_1
-- args: ["[0,0,1]",null,1]

-- field: createManyEmbeddings
WITH __mut AS (
  INSERT INTO "public"."embeddings" ("embedding", "half")
  VALUES ($1::"ext".vector, DEFAULT),
         (DEFAULT, $2::"ext".halfvec)
  RETURNING 1
)
SELECT json_build_object('affectedCount', (SELECT count(*) FROM __mut)) AS data
-- args: ["[1,1,1]","[2,2,2]"]

