-- field: allEmbeddings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY (__t_1."embedding") OPERATOR("ext".<->) $1::"ext".vector ASC, __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  ORDER BY (__t_1."embedding") OPERATOR("ext".<->) $1::"ext".vector ASC, __t_1."id" ASC
  LIMIT 4
) AS __s_2
GROUP BY ()
-- args: ["[1,0.5,-2]",3]

-- field: allEmbeddings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY (__t_1."embedding") OPERATOR("ext".<#>) $1::"ext".vector ASC, (__t_1."embedding") OPERATOR("ext".<=>) $2::"ext".vector DESC NULLS LAST, (__t_1."embedding") OPERATOR("ext".<+>) $3::"ext".vector ASC, (__t_1."half") OPERATOR("ext".<->) $4::"ext".halfvec ASC, (__t_1."sparse") OPERATOR("ext".<=>) $5::"ext".sparsevec ASC, __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  ORDER BY (__t_1."embedding") OPERATOR("ext".<#>) $1::"ext".vector ASC, (__t_1."embedding") OPERATOR("ext".<=>) $2::"ext".vector DESC NULLS LAST, (__t_1."embedding") OPERATOR("ext".<+>) $3::"ext".vector ASC, (__t_1."half") OPERATOR("ext".<->) $4::"ext".halfvec ASC, (__t_1."sparse") OPERATOR("ext".<=>) $5::"ext".sparsevec ASC, __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["[1,2,3]","[1,2,3]","[1,2,3]","[0.25,0.5,1]","{1:1,3:2}/3",100]

-- field: allEmbeddings
-- error: compile: orderBy EMBEDDING_L2_DISTANCE_ASC requires near: {embedding: …}

-- field: allEmbeddings
-- error: compile: near: vector value must have at least one element

-- field: allEmbeddings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  WHERE __t_1."embedding" IS NOT NULL AND __t_1."sparse" IS NULL
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [100]

