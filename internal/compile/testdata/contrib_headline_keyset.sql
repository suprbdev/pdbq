-- field: allDocuments
SELECT json_build_object('edges', coalesce(json_agg(json_build_object('cursor', __s_2.__cursor, 'node', __s_2.edata) ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'searchHeadline', ts_headline(__t_1."notes", to_tsquery($3::text), $5::text)) AS edata, replace(encode(convert_to(jsonb_build_array('Document'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY ts_rank(__t_1."search", to_tsquery($3::text)) DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((ts_rank(__t_1."search", to_tsquery($3::text)) < (SELECT ts_rank(__a."search", to_tsquery($3::text)) FROM "public"."documents" AS __a WHERE __a."id" = $1) OR (ts_rank(__t_1."search", to_tsquery($3::text)) IS NULL AND (SELECT ts_rank(__a."search", to_tsquery($3::text)) FROM "public"."documents" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR (ts_rank(__t_1."search", to_tsquery($3::text)) IS NOT DISTINCT FROM (SELECT ts_rank(__a."search", to_tsquery($3::text)) FROM "public"."documents" AS __a WHERE __a."id" = $1) AND __t_1."id" > $2)) AND (__t_1."search") @@ to_tsquery($3::text)
  ORDER BY ts_rank(__t_1."search", to_tsquery($3::text)) DESC NULLS LAST, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["3","3","cat",2,"MaxWords=5, MinWords=1"]

