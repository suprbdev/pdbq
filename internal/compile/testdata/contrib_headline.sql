-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'searchRank', ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text)), 'snippet', ts_headline($2::regconfig, __t_1."notes", websearch_to_tsquery($2::regconfig, $1::text), $4::text), 'marked', ts_headline($2::regconfig, __t_1."title", websearch_to_tsquery($2::regconfig, $1::text), $5::text)) AS ndata, row_number() OVER (ORDER BY ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."search") @@ websearch_to_tsquery($2::regconfig, $1::text)
  ORDER BY ts_rank(__t_1."search", websearch_to_tsquery($2::regconfig, $1::text)) DESC NULLS LAST, __t_1."id" ASC
  LIMIT 6
) AS __s_2
GROUP BY ()
-- args: ["cat","english",5,"","MaxWords=10, MinWords=3, MaxFragments=2, StartSel=\"\u003cmark\u003e\", StopSel=\"\u003c/mark\u003e\""]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('searchHeadline', NULL::text) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((__t_1."search") @@ to_tsquery($1::text) OR __t_1."id" = $2)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["x",1,100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('keywordsHeadline', ts_headline(__t_1."notes", to_tsquery($1::text), $3::text)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (__t_1."keywords") @@ to_tsquery($1::text)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["pg",100,"HighlightAll=true"]

