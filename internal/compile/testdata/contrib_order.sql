-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'searchRank', ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text))) AS ndata, row_number() OVER (ORDER BY ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."search") @@ websearch_to_tsquery($2::regconfig, $1::text)
  ORDER BY ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["cat","simple",101,100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'searchRank', NULL::float4) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

-- field: allDocuments
-- error: compile: orderBy SEARCH_RANK_DESC requires a matches filter on the same column

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'label', __t_1."label") AS ndata, row_number() OVER (ORDER BY (__t_1."label") OPERATOR("ext".<->) $1::text ASC, (__t_1."label") OPERATOR("ext".<->>) $2::text DESC NULLS LAST, (__t_1."label") OPERATOR("ext".<->>>) $3::text ASC, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY (__t_1."label") OPERATOR("ext".<->) $1::text ASC, (__t_1."label") OPERATOR("ext".<->>) $2::text DESC NULLS LAST, (__t_1."label") OPERATOR("ext".<->>>) $3::text ASC, __t_1."id" ASC
  LIMIT $4
) AS __s_2
GROUP BY ()
-- args: ["postgres","postgres","postgres",6,5]

-- field: allDocuments
-- error: compile: orderBy LABEL_SIMILARITY_DESC requires a similarTo: reference for the same column

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $7), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'keywordsRank', ts_rank(__t_1."keywords", to_tsquery($2::text)), 'searchRank', ts_rank(__t_1."search", plainto_tsquery($1::text))) AS ndata, row_number() OVER (ORDER BY ts_rank(__t_1."keywords", to_tsquery($2::text)) DESC NULLS LAST, ts_rank(__t_1."search", plainto_tsquery($1::text)) DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((__t_1."search") @@ plainto_tsquery($1::text)) AND (__t_1."keywords") @@ to_tsquery($2::text) AND NOT ((__t_1."keywords") @@ to_tsquery($3::text)) AND ((__t_1."search") @@ to_tsquery($4::text) OR __t_1."id" = $5)
  ORDER BY ts_rank(__t_1."keywords", to_tsquery($2::text)) DESC NULLS LAST, ts_rank(__t_1."search", plainto_tsquery($1::text)) DESC NULLS LAST, __t_1."id" ASC
  LIMIT $6
) AS __s_2
GROUP BY ()
-- args: ["conjunct","kept","negated","either",1,101,100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('searchRank', NULL::float4) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE NOT ((__t_1."search") @@ to_tsquery($1::text))
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["x",101,100]

