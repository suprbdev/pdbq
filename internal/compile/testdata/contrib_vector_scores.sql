-- field: allEmbeddings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'embeddingCosineDistance', (__t_1."embedding") OPERATOR("ext".<=>) $1::"ext".vector, 'embeddingL2Distance', (__t_1."embedding") OPERATOR("ext".<->) $2::"ext".vector, 'embeddingNegativeInnerProduct', (__t_1."embedding") OPERATOR("ext".<#>) $3::"ext".vector, 'sparseL1Distance', (__t_1."sparse") OPERATOR("ext".<+>) $4::"ext".sparsevec, 'codeHammingDistance', (__t_1."code") OPERATOR("ext".<~>) $5::varbit, 'codeJaccardDistance', (__t_1."code") OPERATOR("ext".<%>) $6::varbit) AS ndata, row_number() OVER (ORDER BY (__t_1."embedding") OPERATOR("ext".<=>) $7::"ext".vector ASC, __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  ORDER BY (__t_1."embedding") OPERATOR("ext".<=>) $7::"ext".vector ASC, __t_1."id" ASC
  LIMIT 4
) AS __s_2
GROUP BY ()
-- args: ["[1,0.5,-2]","[1,0.5,-2]","[1,0.5,-2]","{1:1}/3","101","101","[1,0.5,-2]",3]

-- field: allEmbeddings
-- error: compile: field halfL2Distance requires near: {half: …}

-- field: allEmbeddings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'codeHammingDistance', (__t_1."code") OPERATOR("ext".<~>) $1::varbit) AS ndata, row_number() OVER (ORDER BY (__t_1."code") OPERATOR("ext".<~>) $2::varbit ASC, __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  ORDER BY (__t_1."code") OPERATOR("ext".<~>) $2::varbit ASC, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["110","110",2]

-- field: allEmbeddings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $9), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'embeddingCosineDistance', (__t_1."embedding") OPERATOR("ext".<=>) $1::"ext".vector) AS ndata, row_number() OVER (ORDER BY (__t_1."embedding") OPERATOR("ext".<=>) $2::"ext".vector ASC, __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  WHERE ((__t_1."code") OPERATOR("ext".<~>) $3::varbit) < $4::float8 AND ((__t_1."embedding") OPERATOR("ext".<=>) $5::"ext".vector) < $6::float8 AND ((__t_1."sparse") OPERATOR("ext".<->) $7::"ext".sparsevec) < $8::float8
  ORDER BY (__t_1."embedding") OPERATOR("ext".<=>) $2::"ext".vector ASC, __t_1."id" ASC
  LIMIT 11
) AS __s_2
GROUP BY ()
-- args: ["[1,0.5,-2]","[1,0.5,-2]","101",2,"[1,0.5,-2]",0.25,"{1:1}/3",2,10]

