-- field: allPosts
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'title', __t_1."title") AS ndata, row_number() OVER (ORDER BY __t_1."title" ASC, __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE __t_1."id" > $1 AND __t_1."title" LIKE $2
  ORDER BY __t_1."title" ASC, __t_1."id" ASC
  LIMIT 11
  OFFSET $3
) AS __s_2
GROUP BY ()
-- args: [1,"Hello%",5,15]

