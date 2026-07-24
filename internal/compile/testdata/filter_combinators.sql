-- field: allUsers
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  WHERE (__t_1."mood" = ANY($1::text[]::"public"."mood"[]) AND (__t_1."email" LIKE $2 OR __t_1."tags" @> $3) AND NOT (__t_1."settings" ? $4))
  ORDER BY __t_1."id" ASC
  LIMIT $5
) AS __s_2
GROUP BY ()
-- args: [["happy","ok"],"%@example.com",["admin"],"banned",101,100]

