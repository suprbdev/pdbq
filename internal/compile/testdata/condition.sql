-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'postsByAuthorId', __l_4.data) AS ndata, __t_1.__rn
  FROM (
    SELECT __t_1."id", row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
    FROM "public"."users" AS __t_1
    WHERE __t_1."email" IS NULL AND __t_1."mood" = $3
    ORDER BY __t_1."id" ASC
    LIMIT 101
  ) AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $2), '[]'::json)) AS data
    FROM (
      SELECT json_build_object('id', __t_2."id") AS ndata, row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM "public"."posts" AS __t_2
      WHERE __t_2."author_id" = __t_1."id" AND __t_2."title" = $1
      ORDER BY __t_2."id" ASC
      LIMIT 101
    ) AS __s_3
    GROUP BY ()
  ) AS __l_4 ON true
) AS __s_5
GROUP BY ()
-- args: ["Hello",100,"happy",100]

