-- field: userById
SELECT jsonb_build_object('id', __t_1."id") AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

-- field: allUsers
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT '{}'::jsonb AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [2,1]

