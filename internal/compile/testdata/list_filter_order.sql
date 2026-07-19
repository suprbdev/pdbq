-- field: allPosts
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'title', __t_1."title") AS ndata, row_number() OVER (ORDER BY __t_1."title" ASC, __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE __t_1."id" > $1 AND __t_1."title" LIKE $2
  ORDER BY __t_1."title" ASC, __t_1."id" ASC
  LIMIT $3
  OFFSET $4
) AS __s_2
GROUP BY ()
-- args: [1,"Hello%",11,5,15]

