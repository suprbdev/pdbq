-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'searchRank', ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text))) AS ndata, row_number() OVER (ORDER BY ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."search") @@ websearch_to_tsquery($2::regconfig, $1::text)
  ORDER BY ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["cat","simple",101,100]

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'searchRank', NULL::float4) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

-- field: allDocuments
-- error: compile: orderBy SEARCH_RANK_DESC requires a matches filter on the same column

-- field: allDocuments
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'label', __t_1."label") AS ndata, row_number() OVER (ORDER BY (__t_1."label") OPERATOR("ext".<->) $1::text ASC, (__t_1."label") OPERATOR("ext".<->>) $2::text DESC NULLS LAST, (__t_1."label") OPERATOR("ext".<->>>) $3::text ASC, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY (__t_1."label") OPERATOR("ext".<->) $1::text ASC, (__t_1."label") OPERATOR("ext".<->>) $2::text DESC NULLS LAST, (__t_1."label") OPERATOR("ext".<->>>) $3::text ASC, __t_1."id" ASC
  LIMIT $4
) AS __s_2
GROUP BY ()
-- args: ["postgres","postgres","postgres",6,5]

-- field: allDocuments
-- error: compile: orderBy LABEL_SIMILARITY_DESC requires a similarTo: reference for the same column

