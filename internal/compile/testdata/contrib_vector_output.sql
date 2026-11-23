-- field: allEmbeddings
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'embedding', (__t_1."embedding")::text::json, 'half', (__t_1."half")::text::json, 'sparse', __t_1."sparse") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."embeddings" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [100]

-- field: scaleVector
SELECT (__fn.v)::text::json AS data FROM (SELECT "public"."scale_vector"("v" => $1::"ext".vector::"ext"."vector", "by" => $2::"pg_catalog"."float8") AS v) AS __fn
-- args: ["[1,2,3]",2]

