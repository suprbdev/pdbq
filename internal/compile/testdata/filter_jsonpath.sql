-- field: allUsers
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  WHERE __t_1."settings" @? $1::jsonpath AND __t_1."settings" @@ $2::jsonpath
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["$.theme","$.age \u003e 18",101,100]

