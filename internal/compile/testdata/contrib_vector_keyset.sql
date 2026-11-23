-- field: allEmbeddings
SELECT json_build_object('edges', coalesce(json_agg(json_build_object('cursor', __s_2.__cursor, 'node', __s_2.edata) ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json), 'pageInfo', json_build_object('hasNextPage', (count(*) > $4))) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS edata, replace(encode(convert_to(jsonb_build_array('Embedding'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY (__t_1."embedding") OPERATOR("ext".<=>) $1::"ext".vector ASC, __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  WHERE (((__t_1."embedding") OPERATOR("ext".<=>) $1::"ext".vector > (SELECT (__a."embedding") OPERATOR("ext".<=>) $1::"ext".vector FROM "public"."embeddings" AS __a WHERE __a."id" = $2) OR ((__t_1."embedding") OPERATOR("ext".<=>) $1::"ext".vector IS NULL AND (SELECT (__a."embedding") OPERATOR("ext".<=>) $1::"ext".vector FROM "public"."embeddings" AS __a WHERE __a."id" = $2) IS NOT NULL)) OR ((__t_1."embedding") OPERATOR("ext".<=>) $1::"ext".vector IS NOT DISTINCT FROM (SELECT (__a."embedding") OPERATOR("ext".<=>) $1::"ext".vector FROM "public"."embeddings" AS __a WHERE __a."id" = $2) AND __t_1."id" > $3))
  ORDER BY (__t_1."embedding") OPERATOR("ext".<=>) $1::"ext".vector ASC, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["[1,2,3]","3","3",2]

